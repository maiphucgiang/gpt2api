package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func internalImageRequest(s *Server, path string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, body)
	request.Header.Set("X-Image-Scheduler-Key", "scheduler-fixture")
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	return response
}

func reserveFixtureImage(t *testing.T, s *Server) string {
	t.Helper()
	response := internalImageRequest(s, "/internal/image-scheduler/reserve", strings.NewReader(`{"model":"gpt-image-2"}`), "application/json")
	var body map[string]any
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil {
		t.Fatalf("reserve: %d %s", response.Code, response.Body.String())
	}
	return stringValue(body["id"])
}

func TestInternalImageExecutionProducesResultsAndReleasesLease(t *testing.T) {
	t.Setenv("GO_IMAGE_SCHEDULER_KEY", "scheduler-fixture")
	fixture := newImageTaskFixture(t)
	fixture.ready.Store(true)
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL = fixture.server.URL
	server := New(cfg)
	defer server.Close()
	_, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"id": "origin", "access_token": "original-at", "source_type": "chatgpt_web"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []bool{false, true} {
		id := reserveFixtureImage(t, server)
		var response *httptest.ResponseRecorder
		if edit {
			var data bytes.Buffer
			form := multipart.NewWriter(&data)
			_ = form.WriteField("model", "gpt-image-2")
			_ = form.WriteField("prompt", "fixture")
			part, _ := form.CreateFormFile("image[]", "fixture.png")
			_, _ = part.Write(append(append([]byte(nil), tinyPNG...), 1))
			_ = form.Close()
			response = internalImageRequest(server, "/internal/image-scheduler/"+id+"/execute-edit", &data, form.FormDataContentType())
		} else {
			response = internalImageRequest(server, "/internal/image-scheduler/"+id+"/execute", strings.NewReader(`{"request":{"prompt":"fixture","model":"gpt-image-2","response_format":"b64_json"}}`), "application/json")
		}
		var body struct {
			Data []map[string]any `json:"data"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Data) != 1 {
			t.Fatalf("execute returned no image: %d %s", response.Code, response.Body.String())
		}
		if !edit && body.Data[0]["b64_json"] == nil {
			t.Fatal("generation response format ignored")
		}
		if release := internalImageRequest(server, "/internal/image-scheduler/"+id+"/release", strings.NewReader(`{}`), "application/json"); release.Code != 200 {
			t.Fatal("release was not idempotent")
		}
		server.schedulerMu.Lock()
		count := len(server.schedulerLeases)
		server.schedulerMu.Unlock()
		if count != 0 {
			t.Fatal("execution leaked its reservation")
		}
	}
	if fixture.submits.Load() != 2 || fixture.uploads.Load() != 1 {
		t.Fatal("internal image flow did not execute")
	}
}

func TestInternalAcceptedGenerationFailureDisablesReplay(t *testing.T) {
	t.Setenv("GO_IMAGE_SCHEDULER_KEY", "scheduler-fixture")
	fixture := newImageTaskFixture(t)
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL = fixture.server.URL
	cfg.RequestTimeout = 100 * time.Millisecond
	server := New(cfg)
	defer server.Close()
	_, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"id": "origin", "access_token": "at-a", "source_type": "chatgpt_web"}, {"id": "other", "access_token": "at-b", "source_type": "chatgpt_web"}})
	if err != nil {
		t.Fatal(err)
	}
	id := reserveFixtureImage(t, server)
	response := internalImageRequest(server, "/internal/image-scheduler/"+id+"/execute", strings.NewReader(`{"request":{"prompt":"fixture","model":"gpt-image-2"}}`), "application/json")
	if response.Code < 400 || response.Header().Get("X-Image-Submission-Uncertain") != "true" || fixture.submits.Load() != 1 {
		t.Fatalf("accepted generation was retried: %d, submissions=%d", response.Code, fixture.submits.Load())
	}
}

func TestInternalPollingRateLimitDoesNotReplaySubmission(t *testing.T) {
	t.Setenv("GO_IMAGE_SCHEDULER_KEY", "scheduler-fixture")
	fixture := newImageTaskFixture(t)
	fixture.rateLimited.Store(true)
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL = fixture.server.URL
	cfg.RequestTimeout = 60 * time.Millisecond
	cfg.ChatMaxRetries = 2
	server := New(cfg)
	defer server.Close()
	_, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"id": "origin", "access_token": "at-a", "source_type": "chatgpt_web"}})
	if err != nil {
		t.Fatal(err)
	}
	id := reserveFixtureImage(t, server)
	response := internalImageRequest(server, "/internal/image-scheduler/"+id+"/execute", strings.NewReader(`{"request":{"prompt":"fixture","model":"gpt-image-2"}}`), "application/json")
	if response.Code != http.StatusTooManyRequests || response.Header().Get("X-Image-Submission-Uncertain") != "true" || fixture.submits.Load() != 1 {
		t.Fatalf("rate-limited poll replayed submission: status=%d submissions=%d body=%s", response.Code, fixture.submits.Load(), response.Body.String())
	}
	items, err := server.store.AccountList()
	if err != nil || len(items) != 1 {
		t.Fatalf("load account feedback: %v %#v", err, items)
	}
	if intValue(items[0]["last_error_status"]) != http.StatusTooManyRequests || stringValue(items[0]["cooldown_until"]) == "" {
		t.Fatalf("429 cooldown was not persisted: %#v", items[0])
	}
}

func TestUnconfiguredInternalSchedulerFailsClosed(t *testing.T) {
	t.Setenv("GO_IMAGE_SCHEDULER_KEY", "")
	server := New(adminTestConfig(t.TempDir()))
	defer server.Close()
	response := internalImageRequest(server, "/internal/image-scheduler/reserve", strings.NewReader(`{}`), "application/json")
	if response.Code != 503 {
		t.Fatal("unconfigured internal scheduler accepted work")
	}
}
