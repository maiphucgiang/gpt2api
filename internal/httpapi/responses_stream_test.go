package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/protocol"
)

func responsesUpstream(t *testing.T, conversation http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<html data-build="test"></html>`)
		case "/backend-api/sentinel/chat-requirements/prepare":
			_, _ = io.WriteString(w, `{"prepare_token":"test"}`)
		case "/backend-api/sentinel/chat-requirements/finalize":
			_, _ = io.WriteString(w, `{"token":"test"}`)
		case "/backend-api/conversation":
			conversation(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func writeResponseFixtureDelta(w http.ResponseWriter, text string) {
	raw, _ := json.Marshal(map[string]any{"message": map[string]any{"author": map[string]string{"role": "assistant"}, "content": map[string]any{"parts": []string{text}}}})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	w.(http.Flusher).Flush()
}

func responseFixtureServer(t *testing.T, upstream string) *Server {
	t.Helper()
	server := New(chatTestConfig(t, upstream))
	t.Cleanup(func() { _ = server.Close() })
	if _, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"id": "account", "access_token": "fixture-at", "source_type": "chatgpt_web"}}); err != nil {
		t.Fatal(err)
	}
	return server
}

func responseEvents(body io.Reader) <-chan map[string]any {
	events := make(chan map[string]any, 64)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var event map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) == nil {
				events <- event
			}
		}
	}()
	return events
}

func TestResponsesDeliversDeltaBeforeUpstreamFinishes(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	upstream := responsesUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeResponseFixtureDelta(w, "first")
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeResponseFixtureDelta(w, "first second")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	server := responseFixtureServer(t, upstream.URL)
	app := httptest.NewServer(server.Handler())
	defer app.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, app.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`))
	request.Header.Set("Authorization", "Bearer api-secret")
	response, err := app.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	events := responseEvents(response.Body)
	sawFirst, completed := false, false
	sequence := 0
	for event := range events {
		if intValue(event["sequence_number"]) != sequence {
			t.Fatal("non-monotonic SSE sequence")
		}
		sequence++
		switch event["type"] {
		case "response.output_text.delta":
			if event["logprobs"] == nil {
				t.Fatal("required text delta field missing")
			}
			if !sawFirst {
				if event["delta"] != "first" {
					t.Fatalf("first delta was buffered: %#v", event)
				}
				sawFirst = true
				unblock()
			}
		case "response.completed":
			completed = true
			value := event["response"].(map[string]any)
			if value["tool_choice"] != "none" || value["tools"] == nil || value["parallel_tool_calls"] != false {
				t.Fatal("required Responses fields missing")
			}
			if value["usage"].(map[string]any)["input_tokens"] == nil {
				t.Fatal("usage was not converted")
			}
		}
	}
	if !sawFirst || !completed {
		t.Fatal("stream did not complete incrementally")
	}
	accounts, _ := server.store.AccountList()
	if intValue(accounts[0]["success"]) != 1 {
		t.Fatal("stream success counted more than once")
	}
}

func TestResponsesCancellationReachesUpstream(t *testing.T) {
	cancelled := make(chan struct{})
	upstream := responsesUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeResponseFixtureDelta(w, "first")
		<-r.Context().Done()
		close(cancelled)
	})
	server := responseFixtureServer(t, upstream.URL)
	app := httptest.NewServer(server.Handler())
	defer app.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, app.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello","stream":true}`))
	request.Header.Set("Authorization", "Bearer api-secret")
	response, err := app.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	for event := range responseEvents(response.Body) {
		if event["type"] == "response.output_text.delta" {
			break
		}
	}
	cancel()
	_ = response.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("client cancellation did not stop the upstream stream")
	}
}

func TestResponsesUpstreamFailureNeverEmitsCompleted(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			upstream := responsesUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				writeResponseFixtureDelta(w, "partial")
				if explicit {
					_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\"fixture failure\"}}\n\n")
				}
			})
			server := responseFixtureServer(t, upstream.URL)
			response := imageTaskRequest(server, http.MethodPost, "/v1/responses", "api-secret", strings.NewReader(`{"model":"gpt-5","input":"hello","stream":true}`))
			body := response.Body.String()
			if !strings.Contains(body, "response.failed") || strings.Contains(body, "response.completed") {
				t.Fatalf("failed stream marked completed: %s", body)
			}
			accounts, _ := server.store.AccountList()
			if intValue(accounts[0]["fail"]) != 1 || intValue(accounts[0]["success"]) != 0 {
				t.Fatal("failed stream feedback incorrect")
			}
		})
	}
}

func TestResponsesAdapterConvertsFragmentedToolArguments(t *testing.T) {
	response := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newResponsesStreamWriter(response, ctx, cancel, protocol.ChatRequest{Model: "fixture"})
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"lookup","arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`,
	}
	for _, chunk := range chunks {
		frame := []byte("data: " + chunk + "\r\n\r\n")
		for len(frame) > 0 {
			size := min(5, len(frame))
			if _, err := stream.Write(frame[:size]); err != nil {
				t.Fatal(err)
			}
			frame = frame[size:]
		}
	}
	_, _ = stream.Write([]byte("data: [DONE]\n\n"))
	events := responseEvents(strings.NewReader(response.Body.String()))
	done := false
	for event := range events {
		if event["type"] == "response.function_call_arguments.done" {
			done = true
			if event["arguments"] != `{"city":"Paris"}` {
				t.Fatal("tool arguments corrupted")
			}
		}
	}
	if !done || strings.Count(response.Body.String(), "event: response.completed") != 1 {
		t.Fatal("tool completion events missing or duplicated")
	}
}

func TestResponsesRejectsUnsupportedCapabilitiesBeforeStreaming(t *testing.T) {
	server := New(adminTestConfig(t.TempDir()))
	defer server.Close()
	for _, extra := range []string{`"tools":[{"type":"function","name":"lookup"}]`, `"previous_response_id":"resp_old"`, `"store":true`, `"text":{"format":{"type":"json_schema"}}`} {
		body := `{"model":"gpt-5","input":"hi","stream":true,` + extra + `}`
		response := imageTaskRequest(server, http.MethodPost, "/v1/responses", "api-secret", strings.NewReader(body))
		if response.Code != 400 || strings.Contains(response.Body.String(), "response.created") {
			t.Fatalf("unsupported option was silently ignored: %s", extra)
		}
	}
}

type disconnectedResponseWriter struct{ header http.Header }

func (w *disconnectedResponseWriter) Header() http.Header     { return w.header }
func (*disconnectedResponseWriter) WriteHeader(int)           {}
func (*disconnectedResponseWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestResponsesWriteFailureCancelsWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newResponsesStreamWriter(&disconnectedResponseWriter{header: make(http.Header)}, ctx, cancel, protocol.ChatRequest{Model: "fixture"})
	if stream.err == nil || ctx.Err() != context.Canceled {
		t.Fatal("downstream write failure did not cancel work")
	}
}

func TestResponsesLengthTerminationIsIncomplete(t *testing.T) {
	response := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newResponsesStreamWriter(response, ctx, cancel, protocol.ChatRequest{Model: "fixture"})
	_, err := stream.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Body.String(), "response.incomplete") || strings.Contains(response.Body.String(), "response.completed") {
		t.Fatal("length-limited output reported completed")
	}
}
