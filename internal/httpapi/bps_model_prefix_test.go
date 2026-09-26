package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBPSModelPrefixAcrossProtocols(t *testing.T) {
	for _, upstream := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.6-terra"} {
		public := "bsg/" + upstream
		for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprint(public, path, stream), func(t *testing.T) {
					var hits atomic.Int32
					s := bpsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
						hits.Add(1)
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if body["model"] != upstream {
							t.Errorf("wire model = %v, want %s", body["model"], upstream)
						}
						w.Header().Set("Content-Type", "text/event-stream")
						bpsFixtureEvent(w, "response.created", map[string]any{"response": map[string]any{"id": "resp_prefix", "model": upstream, "status": "in_progress"}})
						bpsFixtureEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": "resp_prefix", "model": upstream, "status": "completed", "output": []any{bpsFixtureMessage()}}})
					})
					body := map[string]any{"model": public, "stream": stream}
					if path == "/v1/responses" {
						body["input"] = "say OK"
					} else {
						body["messages"] = []any{map[string]any{"role": "user", "content": "say OK"}}
					}
					w := bpsRequest(s, path, s.cfg.APIKey, body)
					if w.Code != 200 || hits.Load() != 1 || !strings.Contains(w.Body.String(), "OK") {
						t.Fatal(w.Code, w.Body.String(), hits.Load())
					}
					modelFields := 0
					var check func(map[string]any)
					check = func(value map[string]any) {
						if actual, present := value["model"]; present {
							modelFields++
							if actual != public {
								t.Errorf("public response model = %v, want %s", actual, public)
							}
						}
						for _, key := range []string{"response", "message"} {
							if child, ok := value[key].(map[string]any); ok {
								check(child)
							}
						}
					}
					if stream {
						for _, line := range strings.Split(w.Body.String(), "\n") {
							if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
								continue
							}
							var event map[string]any
							if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
								t.Fatal(err)
							}
							check(event)
						}
					} else {
						var response map[string]any
						if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
							t.Fatal(err)
						}
						check(response)
					}
					if modelFields == 0 {
						t.Fatal("response omitted model")
					}
				})
			}
		}
	}
}

func TestBPSCatalogOnlyPublishesPrefixedNames(t *testing.T) {
	s := bpsTestServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unprefixed model reached upstream") })
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	s.listModels(w, r)
	var catalog struct {
		Data []struct{ ID string } `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, item := range catalog.Data {
		ids[item.ID] = true
	}
	for _, name := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.6-terra"} {
		if !ids["bsg/"+name] || ids[name] {
			t.Fatalf("incorrect public catalog for %s", name)
		}
		for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
			body := map[string]any{"model": name, "input": "hello", "messages": []any{map[string]any{"role": "user", "content": "hello"}}}
			if response := bpsRequest(s, path, s.cfg.APIKey, body); response.Code != 404 {
				t.Fatalf("unprefixed %s accepted by %s: %d", name, path, response.Code)
			}
		}
	}
	if !ids["gpt-5-6-sol"] {
		t.Fatal("Web model disappeared")
	}
}
