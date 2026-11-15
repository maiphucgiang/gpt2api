package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/maiphucgiang/gpt2api/internal/provider"
)

type searchUpstreamRecorder struct {
	mu           sync.Mutex
	prepare      map[string]any
	conversation map[string]any
	called       []string
}

func searchUpstream(t *testing.T, conversation any, recorder *searchUpstreamRecorder) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if recorder != nil {
			recorder.mu.Lock()
			recorder.called = append(recorder.called, r.Method+" "+r.URL.Path)
			recorder.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html data-build="test-build"></html>`))
		case "/backend-api/sentinel/chat-requirements/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"prepare_token": "test"})
		case "/backend-api/sentinel/chat-requirements/finalize":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "test"})
		case "/backend-api/f/conversation/prepare":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("prepare body: %v", err)
			}
			if recorder != nil {
				recorder.mu.Lock()
				recorder.prepare = body
				recorder.mu.Unlock()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"conduit_token": "conduit"})
		case "/backend-api/f/conversation":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("conversation body: %v", err)
			}
			if recorder != nil {
				recorder.mu.Lock()
				recorder.conversation = body
				recorder.mu.Unlock()
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"conversation_id\":\"conversation-search\"}\n\ndata: [DONE]\n\n")
		case "/backend-api/conversation/conversation-search":
			if got := r.Header.Get("X-OpenAI-Target-Route"); got != "/backend-api/conversation/{conversation_id}" {
				t.Errorf("search poll route header: %q", got)
			}
			_ = json.NewEncoder(w).Encode(conversation)
		default:
			t.Errorf("unexpected GPT upstream path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func searchConversationDocument(answer string, extras map[string]any) map[string]any {
	message := map[string]any{
		"author":      map[string]any{"role": "assistant"},
		"create_time": 2.0,
		"metadata": map[string]any{
			"finish_details": map[string]any{"type": "finished_successfully"},
		},
		"content": map[string]any{"content_type": "text", "parts": []any{answer}},
	}
	for key, value := range extras {
		if key == "metadata" {
			metadata, _ := message["metadata"].(map[string]any)
			for nestedKey, nestedValue := range value.(map[string]any) {
				metadata[nestedKey] = nestedValue
			}
			continue
		}
		message[key] = value
	}
	return map[string]any{
		"mapping": map[string]any{
			"assistant": map[string]any{"message": message},
		},
	}
}

func TestSearchAPIEnablesUpstreamSearchAndReturnsRealSources(t *testing.T) {
	recorder := &searchUpstreamRecorder{}
	answer := "Rust 1.80 is out\ue200cite\ue202search0\ue201.\ue200image_group\ue202{\"query\":[\"rust logo\"]}\ue201"
	upstream := searchUpstream(t, searchConversationDocument(answer, map[string]any{
		"metadata": map[string]any{
			"model_slug": "gpt-5-5",
			"content_references": []any{
				map[string]any{"title": "Rust Blog", "url": "https://blog.rust-lang.org/2024/", "snippet": "Rust 1.80", "type": "webpage"},
			},
		},
	}), recorder)
	defer upstream.Close()
	server := New(chatTestConfig(t, upstream.URL))
	t.Cleanup(func() { _ = server.Close() })
	if _, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"access_token": "test-token", "source_type": "chatgpt_web"}}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"prompt":"latest rust release"}`))
	request.Header.Set("Authorization", "Bearer api-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("response: %d %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["conversation_id"] != "conversation-search" {
		t.Fatalf("conversation_id: %#v", payload["conversation_id"])
	}
	if payload["model"] != "gpt-5-5" {
		t.Fatalf("model: %#v", payload["model"])
	}
	if payload["status"] != "finished_successfully" {
		t.Fatalf("status: %#v", payload["status"])
	}
	if !strings.Contains(stringValue(payload["answer"]), "[1]") {
		t.Fatalf("answer: %#v", payload["answer"])
	}
	sources, _ := payload["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources: %#v", payload["sources"])
	}
	source, _ := sources[0].(map[string]any)
	if source["url"] != "https://blog.rust-lang.org/2024/" || source["title"] != "Rust Blog" {
		t.Fatalf("source: %#v", source)
	}
	groups, _ := payload["image_groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("image_groups: %#v", payload["image_groups"])
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	hints, _ := recorder.prepare["system_hints"].([]any)
	if len(hints) != 1 || hints[0] != "search" {
		t.Fatalf("prepare hints: %#v", recorder.prepare["system_hints"])
	}
	if recorder.prepare["model"] != provider.OpenAISearchModel {
		t.Fatalf("prepare model: %#v", recorder.prepare["model"])
	}
	if recorder.conversation["force_use_search"] != true {
		t.Fatalf("force_use_search: %#v", recorder.conversation["force_use_search"])
	}
	if recorder.conversation["client_reported_search_source"] != "conversation_composer_web_icon" {
		t.Fatalf("search source: %#v", recorder.conversation["client_reported_search_source"])
	}
	if _, ok := recorder.conversation["thinking_effort"]; ok {
		t.Fatal("search conversation sent thinking_effort")
	}
}

func TestSearchAPIEmptyResultOmitsDeadContracts(t *testing.T) {
	upstream := searchUpstream(t, searchConversationDocument("", nil), nil)
	defer upstream.Close()
	server := New(chatTestConfig(t, upstream.URL))
	t.Cleanup(func() { _ = server.Close() })
	if _, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"access_token": "test-token", "source_type": "chatgpt_web"}}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"prompt":"no results"}`))
	request.Header.Set("Authorization", "Bearer api-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("response: %d %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["conversation_id"] != "conversation-search" {
		t.Fatalf("conversation_id: %#v", payload["conversation_id"])
	}
	sources, _ := payload["sources"].([]any)
	if len(sources) != 0 {
		t.Fatalf("sources: %#v", payload["sources"])
	}
	if _, ok := payload["image_groups"]; ok {
		t.Fatalf("empty image_groups should be omitted: %#v", payload["image_groups"])
	}
	if _, ok := payload["model"]; ok {
		t.Fatalf("missing upstream model should be omitted: %#v", payload["model"])
	}
}

func TestSearchAPIRejectsUnknownModelWithoutUpstreamCall(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	server := New(chatTestConfig(t, upstream.URL))
	t.Cleanup(func() { _ = server.Close() })
	request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"prompt":"hi","model":"gpt-4o-search-preview"}`))
	request.Header.Set("Authorization", "Bearer api-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatalf("response: %d %s", response.Code, response.Body.String())
	}
	if called {
		t.Fatal("unknown search slug was forwarded upstream")
	}
	if strings.Contains(response.Body.String(), "gpt-4o-search-preview") && strings.Contains(response.Body.String(), `"model"`) && !strings.Contains(response.Body.String(), "model not found") {
		t.Fatalf("unknown slug was echoed: %s", response.Body.String())
	}
}

func TestOpenAISearchAPIResponseOmitsEmptySessionAndImageGroups(t *testing.T) {
	payload := openAISearchAPIResponse(provider.OpenAISearchResult{
		Answer:  "done",
		Sources: []provider.OpenAISearchSource{},
	})
	if _, ok := payload["conversation_id"]; ok {
		t.Fatal("empty conversation_id should be omitted")
	}
	if _, ok := payload["image_groups"]; ok {
		t.Fatal("empty image_groups should be omitted")
	}
	if _, ok := payload["model"]; ok {
		t.Fatal("empty model should be omitted")
	}
	sources, _ := payload["sources"].([]any)
	if sources == nil {
		t.Fatal("sources must be an empty list, not omitted")
	}
}
