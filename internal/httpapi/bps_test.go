package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
)

func bpsTestToken(id string, expiry time.Time) string {
	raw, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": id}, "exp": expiry.Unix()})
	return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".test"
}
func bpsTestServer(t *testing.T, handler http.HandlerFunc) *Server {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	cfg := adminTestConfig(t.TempDir())
	cfg.BPSEnabled = true
	cfg.BPSBaseURL = upstream.URL
	cfg.BPSTimeout = 2 * time.Second
	s := New(cfg)
	t.Cleanup(func() { _ = s.Close() })
	_, _, _, err := s.store.AddAccounts(nil, []map[string]any{{"id": "account", "source_type": "chatgpt_web", "access_token": bpsTestToken("workspace", time.Now().Add(time.Hour)), "status": "限流", "quota": 0, "enabled": true, "success": 4, "fail": 2}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func bpsRequest(s *Server, path, key string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	switch path {
	case "/v1/responses":
		s.responses(w, r)
	case "/v1/chat/completions":
		s.chatCompletions(w, r)
	case "/v1/messages":
		s.messages(w, r)
	}
	return w
}
func bpsFixtureEvent(w http.ResponseWriter, event string, value map[string]any) {
	value["type"] = event
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, bpsJSON(value))
	w.(http.Flusher).Flush()
}
func bpsFixture(w http.ResponseWriter, output []any) {
	w.Header().Set("Content-Type", "text/event-stream")
	bpsFixtureEvent(w, "response.created", map[string]any{"response": map[string]any{"id": "resp_fixture", "status": "in_progress", "created_at": 1}})
	bpsFixtureEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": "resp_fixture", "object": "response", "created_at": 1, "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 21, "output_tokens": 8, "total_tokens": 29}}})
}
func bpsFixtureMessage() map[string]any {
	return map[string]any{"type": "message", "id": "msg_fixture", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "OK", "annotations": []any{}}}}
}

func TestBPSRoutesIgnoreImageQuotaAndUseRealUsage(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprint(path, stream), func(t *testing.T) {
				var hits atomic.Int32
				s := bpsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					if r.URL.Path != "/basispoints/api/responses" {
						t.Error("used Web route")
					}
					bpsFixture(w, []any{bpsFixtureMessage()})
				})
				body := map[string]any{"model": "bsg/gpt-6-astra", "stream": stream}
				if path == "/v1/responses" {
					body["input"] = "say OK"
				} else {
					body["messages"] = []any{map[string]any{"role": "user", "content": "say OK"}}
				}
				w := bpsRequest(s, path, s.cfg.APIKey, body)
				if w.Code != 200 || !strings.Contains(w.Body.String(), "OK") || hits.Load() != 1 {
					t.Fatal(w.Code, w.Body.String(), hits.Load())
				}
				if !strings.Contains(w.Body.String(), "21") || !strings.Contains(w.Body.String(), "8") {
					t.Fatal("actual usage lost", w.Body.String())
				}
				rows, _ := s.store.AccountList()
				a := rows[0]
				if intValue(a["success"]) != 5 || intValue(a["fail"]) != 2 || intValue(a["quota"]) != 0 || a["status"] != "限流" {
					t.Fatal("BPS changed image availability or counted twice", a["success"], a["fail"], a["status"])
				}
			})
		}
	}
}

func TestBPSForbiddenNeverRetriesOrInvalidatesWebAccount(t *testing.T) {
	var hits atomic.Int32
	s := bpsTestServer(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.Error(w, "not entitled", 403) })
	s.cfg.ChatMaxRetries = 3
	s.cfg.ChatRetryCodes = map[int]bool{403: true}
	rows, _ := s.store.AccountList()
	token := stringValue(rows[0]["access_token"])
	_, _, _ = s.store.UpdateAccount(token, map[string]any{"status": "正常", "quota": 10})
	w := bpsRequest(s, "/v1/responses", s.cfg.APIKey, map[string]any{"model": "bsg/gpt-6-astra", "input": "hello"})
	if w.Code != 403 || hits.Load() != 1 {
		t.Fatal(w.Code, hits.Load(), w.Body.String())
	}
	lease, err := s.accountPool.Reserve(context.Background(), []string{"basic"}, nil)
	if err != nil {
		t.Fatal("BPS failure cooled Web account", err)
	}
	s.accountPool.Release(lease)
	rows, _ = s.store.AccountList()
	if rows[0]["status"] != "正常" || intValue(rows[0]["fail"]) != 3 {
		t.Fatal("account invalidated or counter incorrect")
	}
	// A denial for Astra does not block another explicitly requested BPS model.
	if !s.bpsAvailable(accounts.Account{Token: token, Fields: rows[0]}, "bsg/gpt-5.6-sol", "") {
		t.Fatal("model-specific failure leaked to other models")
	}
}

func TestBPSDisabledModelsHiddenAndUnroutable(t *testing.T) {
	cfg := adminTestConfig(t.TempDir())
	s := New(cfg)
	defer s.Close()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	s.listModels(w, r)
	if strings.Contains(w.Body.String(), "bsg/gpt-6-astra") || strings.Contains(w.Body.String(), "bsg/gpt-5.6-sol") {
		t.Fatal("disabled BPS model advertised")
	}
	result := bpsRequest(s, "/v1/responses", s.cfg.APIKey, map[string]any{"model": "bsg/gpt-6-astra", "input": "hello"})
	if result.Code != 404 {
		t.Fatal(result.Code, result.Body.String())
	}
}

func TestBPSToolRoundTripAcrossAllProtocols(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			var attempts atomic.Int32
			native := map[string]any{"id": "fc_original", "type": "function_call", "call_id": "call_original", "name": "run_officejs", "status": "completed", "arguments": bpsJSON(map[string]any{"references": []any{"lookup"}, "code": `{"key":"hello"}`})}
			s := bpsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if attempts.Add(1) == 1 {
					bpsFixture(w, []any{native})
					return
				}
				found := false
				for _, raw := range bpsArray(body["input"]) {
					item := bpsObject(raw)
					if item["type"] == "function_call" {
						found = true
						if item["id"] != "fc_original" || item["name"] != "run_officejs" {
							t.Error("native identity lost")
						}
					}
				}
				if !found {
					t.Error("tool history not replayed")
				}
				if bpsObject(body["metadata"])["agent_iteration"] != "2" {
					t.Error("tool result treated as new turn")
				}
				bpsFixture(w, []any{bpsFixtureMessage()})
			})
			params := map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}, "required": []any{"key"}}
			body := map[string]any{"model": "bsg/gpt-6-astra"}
			switch path {
			case "/v1/responses":
				body["input"] = "lookup hello"
				body["tools"] = []any{map[string]any{"type": "function", "name": "lookup", "parameters": params}}
			case "/v1/chat/completions":
				body["messages"] = []any{map[string]any{"role": "user", "content": "lookup hello"}}
				body["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": params}}}
			case "/v1/messages":
				body["messages"] = []any{map[string]any{"role": "user", "content": "lookup hello"}}
				body["tools"] = []any{map[string]any{"name": "lookup", "input_schema": params}}
			}
			first := bpsRequest(s, path, s.cfg.APIKey, body)
			if first.Code != 200 || strings.Contains(first.Body.String(), "run_officejs") {
				t.Fatal(first.Code, first.Body.String())
			}
			var response map[string]any
			_ = json.Unmarshal(first.Body.Bytes(), &response)
			switch path {
			case "/v1/responses":
				body["input"] = []any{map[string]any{"role": "user", "content": "lookup hello"}, bpsArray(response["output"])[0], map[string]any{"type": "function_call_output", "call_id": "call_original", "output": "found"}}
			case "/v1/chat/completions":
				msg := bpsObject(bpsArray(response["choices"])[0])["message"]
				body["messages"] = []any{map[string]any{"role": "user", "content": "lookup hello"}, msg, map[string]any{"role": "tool", "tool_call_id": "call_original", "content": "found"}}
			case "/v1/messages":
				body["messages"] = []any{map[string]any{"role": "user", "content": "lookup hello"}, map[string]any{"role": "assistant", "content": response["content"]}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call_original", "content": "found"}}}}
			}
			denied := bpsRequest(s, path, s.cfg.AdminKey, body)
			if denied.Code != 409 || attempts.Load() != 1 {
				t.Fatal("cross-user tool replay reached upstream", denied.Code, attempts.Load())
			}
			second := bpsRequest(s, path, s.cfg.APIKey, body)
			if second.Code != 200 || !strings.Contains(second.Body.String(), "OK") || attempts.Load() != 2 {
				t.Fatal(second.Code, second.Body.String(), attempts.Load())
			}
		})
	}
}

func TestBPSStreamFailureNeverLooksCompleted(t *testing.T) {
	s := bpsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		bpsFixtureEvent(w, "response.created", map[string]any{"response": map[string]any{"id": "resp_cut", "status": "in_progress"}})
		bpsFixtureEvent(w, "response.output_text.delta", map[string]any{"item_id": "msg", "output_index": 0, "content_index": 0, "delta": "partial"})
	})
	w := bpsRequest(s, "/v1/responses", s.cfg.APIKey, map[string]any{"model": "bsg/gpt-6-astra", "input": "hello", "stream": true})
	if !strings.Contains(w.Body.String(), "response.failed") || strings.Contains(w.Body.String(), "response.completed") {
		t.Fatal(w.Body.String())
	}
	rows, _ := s.store.AccountList()
	if intValue(rows[0]["success"]) != 4 || intValue(rows[0]["fail"]) != 3 {
		t.Fatal("stream failure counted as success")
	}
}
