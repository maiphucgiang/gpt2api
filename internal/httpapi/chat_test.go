package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maiphucgiang/gpt2api/internal/config"
)

func chatTestConfig(t *testing.T, upstream string) config.Config {
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL = upstream
	cfg.ChatMaxRetries = 2
	cfg.ChatRetryCodes = map[int]bool{401: true, 429: true, 500: true, 502: true, 503: true}
	return cfg
}

func chatUpstream(t *testing.T, rejectFirst bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html data-build="test-build"></html>`))
		case "/backend-api/sentinel/chat-requirements/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"prepare_token": "test"})
		case "/backend-api/sentinel/chat-requirements/finalize":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "test"})
		case "/backend-api/conversation":
			if rejectFirst && r.Header.Get("Authorization") == "Bearer first-token" {
				http.Error(w, "temporary failure", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"message\":{\"author\":{\"role\":\"assistant\"},\"content\":{\"content_type\":\"text\",\"parts\":[\"hello\"]}}}\n\ndata: [DONE]\n\n"))
		default:
			t.Errorf("unexpected GPT upstream path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func TestGPTChatCompatibilityAndSingleSuccessFeedback(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"chat", "/v1/chat/completions", `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`},
		{"chat_stream", "/v1/chat/completions", `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", `{"model":"gpt-5","input":"hi"}`},
		{"responses_stream", "/v1/responses", `{"model":"gpt-5","stream":true,"input":"hi"}`},
		{"messages", "/v1/messages", `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`},
		{"messages_stream", "/v1/messages", `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := chatUpstream(t, false)
			defer upstream.Close()
			server := New(chatTestConfig(t, upstream.URL))
			t.Cleanup(func() { _ = server.Close() })
			if _, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"access_token": "test-token", "source_type": "chatgpt_web"}}); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer api-secret")
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != 200 || !strings.Contains(response.Body.String(), "hello") {
				t.Fatalf("response: %d %s", response.Code, response.Body.String())
			}
			items, _ := server.store.AccountList()
			if intValue(items[0]["success"]) != 1 || intValue(items[0]["fail"]) != 0 {
				t.Fatalf("wrong feedback count: %v/%v", items[0]["success"], items[0]["fail"])
			}
		})
	}
}

func TestGPTChatOmitsThinkingEffort(t *testing.T) {
	var conversation map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html data-build="test-build"></html>`))
		case "/backend-api/sentinel/chat-requirements/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"prepare_token": "test"})
		case "/backend-api/sentinel/chat-requirements/finalize":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "test"})
		case "/backend-api/conversation":
			if err := json.NewDecoder(r.Body).Decode(&conversation); err != nil {
				t.Errorf("conversation body: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"message\":{\"author\":{\"role\":\"assistant\"},\"content\":{\"content_type\":\"text\",\"parts\":[\"hello\"]}}}\n\ndata: [DONE]\n\n"))
		default:
			t.Errorf("unexpected GPT upstream path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	server := New(chatTestConfig(t, upstream.URL))
	t.Cleanup(func() { _ = server.Close() })
	if _, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"access_token": "test-token", "source_type": "chatgpt_web"}}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer api-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("response: %d %s", response.Code, response.Body.String())
	}
	if _, ok := conversation["thinking_effort"]; ok {
		t.Fatalf("web chat sent thinking_effort: %#v", conversation["thinking_effort"])
	}
}

func TestGPTChatRejectsImageContent(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer upstream.Close()
	server := New(chatTestConfig(t, upstream.URL))
	t.Cleanup(func() { _ = server.Close() })
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]}]}`))
	request.Header.Set("Authorization", "Bearer api-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "image content is not supported for web chat") {
		t.Fatalf("response: %d %s", response.Code, response.Body.String())
	}
	if called {
		t.Fatal("unsupported image content was forwarded upstream")
	}
}

func TestGPTChatRetryCountsEachAccountOnce(t *testing.T) {
	upstream := chatUpstream(t, true)
	defer upstream.Close()
	server := New(chatTestConfig(t, upstream.URL))
	t.Cleanup(func() { _ = server.Close() })
	if _, _, _, err := server.store.AddAccounts(nil, []map[string]any{
		{"access_token": "first-token", "source_type": "chatgpt_web"},
		{"access_token": "second-token", "source_type": "chatgpt_web"},
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer api-secret")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("response: %d %s", response.Code, response.Body.String())
	}
	items, _ := server.store.AccountList()
	if intValue(items[0]["fail"]) != 1 || intValue(items[1]["success"]) != 1 {
		t.Fatal("retry feedback lost or duplicated")
	}
}
