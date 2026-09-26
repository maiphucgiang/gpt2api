package bps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
)

func testAccount() accounts.Account {
	claims := Object{"https://api.openai.com/auth": Object{"chatgpt_account_id": "workspace"}, "exp": time.Now().Add(time.Hour).Unix()}
	token := "e30." + base64.RawURLEncoding.EncodeToString(encode(claims)) + ".test"
	return accounts.Account{Token: token, Fields: Object{"id": "account", "source_type": "chatgpt_web"}}
}
func testSource() Object {
	return Object{"model": "bsg/gpt-6-astra", "input": "hello", "tools": []any{Object{"type": "function", "name": "read_file", "parameters": Object{"type": "object", "properties": Object{"path": Object{"type": "string"}}, "required": []any{"path"}, "additionalProperties": false}}}}
}
func prepareTest(t *testing.T, source Object, replay *Replay) *Prepared {
	t.Helper()
	catalog, err := Validate(source)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(source, "owner", "account", catalog, replay)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}
func nativeCall(call, name, payload string) Object {
	return Object{"id": "fc_" + call, "type": "function_call", "name": "run_officejs", "call_id": call, "status": "completed", "arguments": string(encode(Object{"references": []any{name}, "code": payload}))}
}
func terminal(items ...any) Object {
	return Object{"id": "resp_test", "model": "gpt-6-astra", "status": "completed", "output": items, "usage": Object{"input_tokens": 17, "output_tokens": 9, "total_tokens": 26}}
}
func sendEvent(w http.ResponseWriter, kind string, value Object) {
	value["type"] = kind
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encode(value))
	w.(http.Flusher).Flush()
}
func startEvent(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	sendEvent(w, "response.created", Object{"response": Object{"id": "resp_test", "status": "in_progress", "model": "gpt-6-astra"}})
}

func TestCredentialsUseWorkspaceNotSubject(t *testing.T) {
	account := testAccount()
	id, _, err := Credentials(account)
	if err != nil || id != "workspace" {
		t.Fatal(id, err)
	}
	account.Fields["chatgpt_account_id"] = "selected-workspace"
	id, _, err = Credentials(account)
	if err != nil || id != "selected-workspace" {
		t.Fatal(id, err)
	}
	account.Fields = Object{"id": "local-id", "user_id": "user-id"}
	account.Token = "e30." + base64.RawURLEncoding.EncodeToString(encode(Object{"sub": "user-id"})) + ".test"
	if _, _, err = Credentials(account); err == nil {
		t.Fatal("used local ID or subject as workspace")
	}
}

func TestToolValidationAndIsolation(t *testing.T) {
	source := testSource()
	catalog, err := Validate(source)
	if err != nil {
		t.Fatal(err)
	}
	valid := nativeCall("call", "read_file", `{"path":"a\\b.txt"}`)
	for _, tc := range []struct {
		name   string
		mutate func(Object)
	}{
		{"malformed", func(n Object) { n["arguments"] = `{"code":` }},
		{"undeclared", func(n Object) { n["arguments"] = string(encode(Object{"references": []any{"exec"}, "code": `{}`})) }},
		{"wrong schema", func(n Object) {
			n["arguments"] = string(encode(Object{"references": []any{"read_file"}, "code": `{"path":3}`}))
		}},
		{"extra property", func(n Object) {
			n["arguments"] = string(encode(Object{"references": []any{"read_file"}, "code": `{"path":"x","shell":"run"}`}))
		}},
		{"bad escape", func(n Object) {
			n["arguments"] = string(encode(Object{"references": []any{"read_file"}, "code": `{"path":"C:\temp\q"}`}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := clone(valid)
			tc.mutate(n)
			if _, err := catalog.convert(n); err == nil {
				t.Fatal("accepted invalid call")
			}
		})
	}
	out, err := catalog.convert(valid)
	if err != nil {
		t.Fatal(err)
	}
	if String(out["name"]) != "read_file" || String(out["call_id"]) != "call" {
		t.Fatal(out)
	}
	source["parallel_tool_calls"] = false
	catalog, _ = Validate(source)
	if _, _, _, err = catalog.convertOutput(terminal(valid, nativeCall("other", "read_file", `{"path":"b"}`))); err == nil {
		t.Fatal("silently dropped a parallel call")
	}
	source["tool_choice"] = "required"
	catalog, _ = Validate(source)
	if _, _, _, err = catalog.convertOutput(terminal(message("assistant", "no tool"))); err == nil {
		t.Fatal("ignored required tool choice")
	}
}

func TestSchemaExternalResourcesAreNotLoaded(t *testing.T) {
	var reached atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1); fmt.Fprint(w, `{"type":"object"}`) }))
	defer remote.Close()
	source := testSource()
	object(list(source["tools"])[0])["parameters"] = Object{"$ref": remote.URL + "/schema"}
	if _, err := Validate(source); err == nil {
		t.Fatal("accepted remote schema")
	}
	if reached.Load() != 0 {
		t.Fatal("schema compiler made a network request")
	}
}

func TestCustomPatchIsBytePreserved(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: a.txt\n+\"C:\\path\"\n*** End Patch\n"
	source := testSource()
	source["tools"] = []any{Object{"type": "custom", "name": "apply_patch", "format": Object{"type": "grammar", "grammar": Object{"syntax": "lark", "definition": "start: /.+/"}}}}
	catalog, err := Validate(source)
	if err != nil {
		t.Fatal(err)
	}
	item, err := catalog.convert(nativeCall("patch", "apply_patch", patch))
	if err != nil {
		t.Fatal(err)
	}
	if item["input"] != patch || item["type"] != "custom_tool_call" {
		t.Fatal("custom input modified", item)
	}
}

func TestReplaySurvivesRestartAndSeparatesTenants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bps.json")
	replay := NewReplay(path)
	source := testSource()
	p := prepareTest(t, source, replay)
	native := nativeCall("call", "read_file", `{"path":"a"}`)
	_, natives, clients, err := p.Tools.convertOutput(terminal(native))
	if err != nil {
		t.Fatal(err)
	}
	if err = replay.save("owner", p.Model, "account", natives, clients); err != nil {
		t.Fatal(err)
	}
	replay = NewReplay(path)
	input := []any{message("user", "hello"), clients[0], Object{"type": "function_call_output", "call_id": "call", "output": "contents"}}
	if _, err = replay.AccountFor("other", p.Model, input); err == nil {
		t.Fatal("cross-tenant replay accepted")
	}
	ref, err := replay.AccountFor("owner", p.Model, input)
	if err != nil || ref != "account" {
		t.Fatal(ref, err)
	}
	source["input"] = input
	continued := prepareTest(t, source, replay)
	if object(continued.Body["metadata"])["turn_id"] != object(p.Body["metadata"])["turn_id"] {
		t.Fatal("tool result changed user turn")
	}
	if object(continued.Body["metadata"])["agent_iteration"] != "2" {
		t.Fatal("iteration did not advance")
	}
	body := list(continued.Body["input"])
	if string(encode(body[2])) != string(encode(native)) {
		t.Fatal("original native tool identity changed")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("replay permissions", err)
	}
	object(input[1])["arguments"] = `{"path":"changed"}`
	if _, err = Prepare(source, "owner", "account", p.Tools, replay); err == nil {
		t.Fatal("changed history was accepted")
	}
}

func TestStableTurnAndParallelRoundIdentity(t *testing.T) {
	replay := NewReplay("")
	source := testSource()
	p := prepareTest(t, source, replay)
	p2 := prepareTest(t, source, replay)
	if string(encode(p.Body)) != string(encode(p2.Body)) {
		t.Fatal("identical requests changed bytes")
	}
	other, err := Prepare(source, "different-owner", "account", p.Tools, replay)
	if err != nil {
		t.Fatal(err)
	}
	if object(other.Body["metadata"])["task_id"] == object(p.Body["metadata"])["task_id"] {
		t.Fatal("task identity crosses callers")
	}
	native := []Object{nativeCall("a", "read_file", `{"path":"a"}`), nativeCall("b", "read_file", `{"path":"b"}`)}
	clients := []Object{}
	for _, n := range native {
		c, _ := p.Tools.convert(n)
		clients = append(clients, c)
	}
	if err = replay.save("owner", p.Model, "account", native, clients); err != nil {
		t.Fatal(err)
	}
	source["input"] = []any{message("user", "hello"), clients[0], clients[1], Object{"type": "function_call_output", "call_id": "a", "output": "a"}, Object{"type": "function_call_output", "call_id": "b", "output": "b"}}
	next := prepareTest(t, source, replay)
	if object(next.Body["metadata"])["agent_iteration"] != "2" {
		t.Fatal("parallel results counted as multiple rounds")
	}
}

func TestHTTPHeadersAndRealStreamingBeforeCompletion(t *testing.T) {
	account := testAccount()
	release := make(chan struct{})
	seen := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/basispoints/api/responses" || r.Header.Get("Chatgpt-Account-Id") != "workspace" || r.Header.Get("X-Openai-Account-Id") != "workspace" || r.Header.Get("X-Basispoints-Auth-Mode") != "chatgpt" {
			t.Error("wrong BPS transport headers")
		}
		if r.Header.Get("Authorization") != "Bearer "+account.Token {
			t.Error("wrong credential")
		}
		var body Object
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "gpt-6-astra" || body["model_selection"] != "explicit" || body["tools"] != nil {
			t.Error("wrong request body")
		}
		startEvent(w)
		sendEvent(w, "response.output_item.added", Object{"output_index": 0, "item": Object{"type": "message", "id": "msg", "role": "assistant", "content": []any{}, "status": "in_progress"}})
		sendEvent(w, "response.content_part.added", Object{"output_index": 0, "item_id": "msg", "content_index": 0, "part": Object{"type": "output_text", "text": ""}})
		sendEvent(w, "response.output_text.delta", Object{"item_id": "msg", "output_index": 0, "content_index": 0, "delta": "hello"})
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		item := message("assistant", "hello")
		item["id"] = "msg"
		item["status"] = "completed"
		sendEvent(w, "response.completed", Object{"response": terminal(item)})
	}))
	defer upstream.Close()
	replay := NewReplay("")
	p := prepareTest(t, testSource(), replay)
	client := NewClient(upstream.URL, 3*time.Second, nil, nil, replay)
	finished := make(chan error, 1)
	go func() {
		_, err := client.Run(context.Background(), account, p, func(e Object) error {
			if e["type"] == "response.output_text.delta" {
				seen <- struct{}{}
			}
			return nil
		})
		finished <- err
	}()
	select {
	case <-seen:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("delta buffered until completion")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestToolStreamNeverLeaksBeforeValidation(t *testing.T) {
	for _, mode := range []string{"valid", "bad_second_tool", "cutoff", "disk_failure"} {
		t.Run(mode, func(t *testing.T) {
			native := nativeCall("a", "read_file", `{"path":"a"}`)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				startEvent(w)
				sendEvent(w, "response.output_item.added", Object{"output_index": 0, "item": native})
				sendEvent(w, "response.function_call_arguments.delta", Object{"delta": native["arguments"]})
				sendEvent(w, "response.output_item.done", Object{"output_index": 0, "item": native})
				if mode == "cutoff" {
					return
				}
				items := []any{native}
				if mode == "bad_second_tool" {
					items = append(items, nativeCall("b", "undeclared", `{}`))
				}
				sendEvent(w, "response.completed", Object{"response": terminal(items...)})
			}))
			defer upstream.Close()
			replay := NewReplay("")
			if mode == "disk_failure" {
				replay.path = t.TempDir()
			}
			p := prepareTest(t, testSource(), replay)
			client := NewClient(upstream.URL, time.Second, nil, nil, replay)
			var events []Object
			_, err := client.Run(context.Background(), testAccount(), p, func(e Object) error { events = append(events, e); return nil })
			calls, completed := 0, 0
			for _, e := range events {
				if e["type"] == "response.completed" {
					completed++
				}
				if toolItem(object(e["item"])) {
					calls++
					if object(e["item"])["name"] != "read_file" {
						t.Fatal("native transport escaped")
					}
				}
			}
			if mode == "valid" {
				if err != nil || calls != 2 || completed != 1 {
					t.Fatal(err, calls, completed)
				}
			} else if err == nil || calls != 0 || completed != 0 {
				t.Fatal("invalid/partial batch became executable", err, calls, completed)
			}
		})
	}
}

func TestTerminalEventWithoutBlankLineAndCancellation(t *testing.T) {
	response := terminal(message("assistant", "OK"))
	raw := fmt.Sprintf("event: response.completed\r\ndata: %s", encode(Object{"type": "response.completed", "response": response}))
	count := 0
	if err := decodeSSE(strings.NewReader(raw), func(Object) error { count++; return nil }); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { startEvent(w); <-r.Context().Done(); close(cancelled) }))
	defer upstream.Close()
	replay := NewReplay("")
	client := NewClient(upstream.URL, time.Second, nil, nil, replay)
	_, err := client.Run(context.Background(), testAccount(), prepareTest(t, testSource(), replay), func(Object) error { return context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach upstream")
	}
}

func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer upstream.Close()
	replay := NewReplay("")
	client := NewClient(upstream.URL, time.Second, nil, nil, replay)
	_, err := client.Run(context.Background(), testAccount(), prepareTest(t, testSource(), replay), nil)
	if Status(err) != 307 || hits.Load() != 0 {
		t.Fatal("followed upstream redirect", err, hits.Load())
	}
}
