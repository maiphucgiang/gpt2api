package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/provider"
)

type imageTaskFixture struct {
	server        *httptest.Server
	ready         atomic.Bool
	submits       atomic.Int32
	uploads       atomic.Int32
	wrongAccount  atomic.Bool
	expectedToken atomic.Value
	hold          chan struct{}
	entered       chan struct{}
}

func newImageTaskFixture(t *testing.T) *imageTaskFixture {
	t.Helper()
	fixture := &imageTaskFixture{entered: make(chan struct{}, 20)}
	fixture.expectedToken.Store("")
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<html data-build="test-build"><script src="/static/app.js"></script></html>`)
		case r.URL.Path == "/backend-api/sentinel/chat-requirements/prepare":
			_ = json.NewEncoder(w).Encode(map[string]string{"prepare_token": "prepare"})
		case r.URL.Path == "/backend-api/sentinel/chat-requirements/finalize":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "requirements"})
		case r.URL.Path == "/backend-api/f/conversation/prepare":
			_ = json.NewEncoder(w).Encode(map[string]string{"conduit_token": "conduit"})
		case r.URL.Path == "/backend-api/f/conversation":
			number := fixture.submits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"conversation_id\":\"conversation-%d\"}\n\n", number)
			w.(http.Flusher).Flush()
			fixture.entered <- struct{}{}
			if fixture.hold != nil {
				select {
				case <-fixture.hold:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case strings.HasPrefix(r.URL.Path, "/backend-api/conversation/"):
			if expected := fixture.expectedToken.Load().(string); expected != "" && r.Header.Get("Authorization") != "Bearer "+expected {
				fixture.wrongAccount.Store(true)
			}
			if fixture.ready.Load() {
				writeHTTPAPIGeneratedImageConversation(w, "file_reference", "file_generated")
			} else {
				_, _ = io.WriteString(w, `{"mapping":{}}`)
			}
		case r.URL.Path == "/backend-api/files":
			fixture.uploads.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"file_id": "file_reference", "upload_url": fixture.server.URL + "/upload"})
		case r.URL.Path == "/upload" || r.URL.Path == "/backend-api/files/file_reference/uploaded":
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, `{}`)
		case r.URL.Path == "/backend-api/files/file_generated/download":
			_ = json.NewEncoder(w).Encode(map[string]string{"download_url": fixture.server.URL + "/blob"})
		case r.URL.Path == "/blob":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNG)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func imageTaskRequest(s *Server, method, path, key string, body io.Reader) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://images.example.test"+path, body)
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	return response
}

func waitImageTask(t *testing.T, s *Server, key, id string) map[string]any {
	t.Helper()
	deadline := time.After(4 * time.Second)
	for {
		response := imageTaskRequest(s, http.MethodGet, "/api/image-tasks/"+id, key, nil)
		if response.Code != 200 {
			t.Fatalf("task lookup: %d %s", response.Code, response.Body.String())
		}
		var value map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if value["status"] == "success" || value["status"] == "error" {
			return value
		}
		select {
		case <-deadline:
			t.Fatal("task did not finish")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestImageTasksIsolateOwnersAndRejectChangedIdempotencyInput(t *testing.T) {
	cfg := adminTestConfig(t.TempDir())
	server := New(cfg)
	defer server.Close()
	_, keyA, err := server.store.CreateKey("user", "A", cfg.AdminKey)
	if err != nil {
		t.Fatal(err)
	}
	_, keyB, err := server.store.CreateKey("user", "B", cfg.AdminKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{keyA, keyB} {
		response := imageTaskRequest(server, http.MethodPost, "/api/image-tasks/generations", key, strings.NewReader(`{"client_task_id":"same-id","prompt":"fixture"}`))
		if response.Code != 202 {
			t.Fatalf("submit: %d %s", response.Code, response.Body.String())
		}
		_ = waitImageTask(t, server, key, "same-id")
	}
	server.imageTaskMu.RLock()
	count := len(server.imageTasks)
	server.imageTaskMu.RUnlock()
	if count != 2 {
		t.Fatal("owners overwrote each other's task")
	}
	same := imageTaskRequest(server, http.MethodPost, "/api/image-tasks/generations", keyA, strings.NewReader(`{"client_task_id":"same-id","prompt":"fixture"}`))
	changed := imageTaskRequest(server, http.MethodPost, "/api/image-tasks/generations", keyA, strings.NewReader(`{"client_task_id":"same-id","prompt":"changed"}`))
	if same.Code != 202 || changed.Code != 409 {
		t.Fatalf("idempotency: %d / %d", same.Code, changed.Code)
	}
	raw, err := os.ReadFile(server.imageTasksPath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(keyA)) || bytes.Contains(raw, []byte(keyB)) {
		t.Fatal("task persistence contains API keys")
	}
	if info, _ := os.Stat(server.imageTasksPath()); info.Mode().Perm() != 0600 {
		t.Fatal("task state permissions are not private")
	}
}

func TestImageTaskQueueRejectsExcessAndShutdownPersistsInterruption(t *testing.T) {
	fixture := newImageTaskFixture(t)
	fixture.hold = make(chan struct{})
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL = fixture.server.URL
	cfg.ImageTaskWorkers, cfg.ImageTaskQueueSize = 1, 1
	server := New(cfg)
	defer server.Close()
	_, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"id": "origin", "access_token": "original-at", "source_type": "chatgpt_web"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		response := imageTaskRequest(server, http.MethodPost, "/api/image-tasks/generations", "api-secret", strings.NewReader(fmt.Sprintf(`{"client_task_id":"task-%d","prompt":"fixture"}`, i)))
		if response.Code != 202 {
			t.Fatalf("submit: %d %s", response.Code, response.Body.String())
		}
		if i == 1 {
			select {
			case <-fixture.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("generation did not start")
			}
		}
	}
	overflow := imageTaskRequest(server, http.MethodPost, "/api/image-tasks/generations", "api-secret", strings.NewReader(`{"client_task_id":"overflow","prompt":"fixture"}`))
	if overflow.Code != 429 {
		t.Fatalf("queue overflow: %d", overflow.Code)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := New(cfg)
	defer restarted.Close()
	for i := 1; i <= 2; i++ {
		task := waitImageTask(t, restarted, "api-secret", fmt.Sprintf("task-%d", i))
		if task["status"] != "error" {
			t.Fatal("interrupted task was replayed")
		}
	}
	if fixture.submits.Load() != 1 {
		t.Fatal("queued work was submitted during shutdown or restart")
	}
}

func TestImageTaskResumeUsesOriginalAccountAfterRestartAndRotation(t *testing.T) {
	t.Setenv("GO_PUBLIC_BASE_URL", "https://images.example.test")
	fixture := newImageTaskFixture(t)
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL = fixture.server.URL
	cfg.RequestTimeout = 100 * time.Millisecond
	server := New(cfg)
	server.openAIImage = provider.NewOpenAIImage(fixture.server.URL, fixture.server.Client(), nil, 100*time.Millisecond)
	defer server.Close()
	_, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"id": "origin", "access_token": "original-at", "source_type": "chatgpt_web"}, {"id": "other", "access_token": "other-at", "source_type": "chatgpt_web"}})
	if err != nil {
		t.Fatal(err)
	}
	response := imageTaskRequest(server, http.MethodPost, "/api/image-tasks/generations", "api-secret", strings.NewReader(`{"client_task_id":"recoverable","prompt":"fixture"}`))
	if response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	task := waitImageTask(t, server, "api-secret", "recoverable")
	if task["can_resume_poll"] != true || fixture.submits.Load() != 1 {
		t.Fatalf("missing checkpoint or duplicate submission: %#v", task)
	}
	if _, _, err := server.store.RotateAccountTokens("original-at", "rotated-at", "", "", map[string]any{"status": "限流", "quota": 0}); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.ready.Store(true)
	fixture.expectedToken.Store("rotated-at")
	restarted := New(cfg)
	defer restarted.Close()
	response = imageTaskRequest(restarted, http.MethodPost, "/api/image-tasks/recoverable/resume-poll", "api-secret", strings.NewReader(`{"extra_timeout_secs":1}`))
	if response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	task = waitImageTask(t, restarted, "api-secret", "recoverable")
	if task["status"] != "success" || fixture.submits.Load() != 1 || fixture.wrongAccount.Load() {
		t.Fatalf("resume failed or regenerated: %#v, submissions=%d", task, fixture.submits.Load())
	}
	if !strings.Contains(fmt.Sprint(task["data"]), "https://images.example.test/") {
		t.Fatal("result contains an internal origin")
	}
}

func TestImageTaskEditsSpoolInputsAndReleaseThemAfterCompletion(t *testing.T) {
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
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("client_task_id", "edit-1")
	_ = form.WriteField("prompt", "fixture")
	_ = form.WriteField("quality", "high")
	part, _ := form.CreateFormFile("image[]", "reference.png")
	_, _ = part.Write(append(append([]byte(nil), tinyPNG...), 1))
	_ = form.Close()
	request := httptest.NewRequest(http.MethodPost, "https://images.example.test/api/image-tasks/edits", &body)
	request.Header.Set("Authorization", "Bearer api-secret")
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 202 {
		t.Fatalf("edit submit: %d %s", response.Code, response.Body.String())
	}
	task := waitImageTask(t, server, "api-secret", "edit-1")
	if task["status"] != "success" || fixture.uploads.Load() != 1 || task["quality"] != "high" {
		t.Fatalf("edit did not execute: %#v", task)
	}
	entries, err := os.ReadDir(filepath.Join(cfg.DataDir, "image_task_inputs"))
	if err != nil || len(entries) != 0 {
		t.Fatal("finished task retained reference input files")
	}
}
