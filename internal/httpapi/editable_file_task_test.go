package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/config"
)

type editableTaskFixture struct {
	server         *httptest.Server
	ready          atomic.Bool
	rateLimited    atomic.Bool
	submits        atomic.Int32
	polls          atomic.Int32
	wrongAccount   atomic.Bool
	expectedToken  atomic.Value
	prepareModel   atomic.Value
	conversationID string
	holdSubmit     chan struct{}
	enteredSubmit  chan struct{}
}

func newEditableTaskFixture(t *testing.T) *editableTaskFixture {
	t.Helper()
	fixture := &editableTaskFixture{conversationID: "conversation-editable"}
	fixture.expectedToken.Store("")
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<html data-build="test-build"><script src="/static/app.js"></script></html>`)
		case r.URL.Path == "/backend-api/sentinel/chat-requirements/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"prepare_token": "prepare-token"})
		case r.URL.Path == "/backend-api/sentinel/chat-requirements/finalize":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "requirements-token"})
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/files":
			_ = json.NewEncoder(w).Encode(map[string]any{"file_id": "file_reference", "upload_url": fixture.server.URL + "/reference-upload"})
		case r.URL.Path == "/reference-upload":
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/uploaded"):
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/backend-api/f/conversation/prepare":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			fixture.prepareModel.Store(fmt.Sprint(payload["model"]))
			_ = json.NewEncoder(w).Encode(map[string]any{"conduit_token": "conduit-token"})
		case r.URL.Path == "/backend-api/f/conversation":
			if expected := fixture.expectedToken.Load().(string); expected != "" && r.Header.Get("Authorization") != "Bearer "+expected {
				fixture.wrongAccount.Store(true)
			}
			fixture.submits.Add(1)
			if fixture.enteredSubmit != nil {
				select {
				case fixture.enteredSubmit <- struct{}{}:
				default:
				}
			}
			if fixture.holdSubmit != nil {
				select {
				case <-fixture.holdSubmit:
				case <-r.Context().Done():
					return
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"conversation_id\":\"%s\"}\n\n", fixture.conversationID)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case strings.HasPrefix(r.URL.Path, "/backend-api/conversation/") && !strings.Contains(r.URL.Path, "/attachment/") && !strings.Contains(r.URL.Path, "/interpreter/"):
			if expected := fixture.expectedToken.Load().(string); expected != "" && r.Header.Get("Authorization") != "Bearer "+expected {
				fixture.wrongAccount.Store(true)
			}
			fixture.polls.Add(1)
			if fixture.rateLimited.Load() {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"detail":"slow down"}`)
				return
			}
			if !fixture.ready.Load() {
				_ = json.NewEncoder(w).Encode(map[string]any{"mapping": map[string]any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"mapping": map[string]any{
				"msg": map[string]any{"message": map[string]any{
					"id": "msg", "author": map[string]any{"role": "assistant"},
					"content": map[string]any{"parts": []any{
						map[string]any{"name": "deck.pptx", "file_id": "file_ppt", "mime_type": "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
						map[string]any{"name": "assets.zip", "file_id": "file_zip", "mime_type": "application/zip"},
					}},
				}},
			}})
		case strings.HasPrefix(r.URL.Path, "/backend-api/files/download/file_ppt"):
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
			_, _ = io.WriteString(w, "PPTX")
		case strings.HasPrefix(r.URL.Path, "/backend-api/files/download/file_zip"):
			w.Header().Set("Content-Type", "application/zip")
			_, _ = io.WriteString(w, "ZIP")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func editableTaskRequest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer api-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	return response
}

func waitEditableTask(t *testing.T, s *Server, id string) map[string]any {
	t.Helper()
	deadline := time.After(6 * time.Second)
	for {
		response := editableTaskRequest(s, http.MethodGet, "/v1/editable-file-tasks/"+id, "")
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
			t.Fatalf("task did not finish: %#v", value)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitEditableCheckpoint(t *testing.T, dataDir string) {
	t.Helper()
	path := filepath.Join(dataDir, "editable_file_tasks.json")
	deadline := time.After(4 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			var envelope struct {
				Tasks []*editableFileTaskState `json:"tasks"`
			}
			if json.Unmarshal(raw, &envelope) == nil {
				for _, task := range envelope.Tasks {
					if editableCheckpointReady(task) {
						return
					}
				}
			}
		}
		select {
		case <-deadline:
			t.Fatal("checkpoint was not persisted")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func newEditableTaskServer(t *testing.T, fixture *editableTaskFixture, accounts ...map[string]any) *Server {
	t.Helper()
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL = fixture.server.URL
	cfg.RequestTimeout = 4 * time.Second
	cfg.EditableModel = "gpt-5-5-thinking"
	cfg.EditableReasoningEffort = "extended"
	server := New(cfg)
	t.Cleanup(func() { _ = server.Close() })
	if len(accounts) > 0 {
		if _, _, _, err := server.store.AddAccounts(nil, accounts); err != nil {
			t.Fatal(err)
		}
	}
	return server
}

func editableImageBody(id, prompt string) string {
	image := base64.StdEncoding.EncodeToString(tinyPNG)
	return fmt.Sprintf(`{"client_task_id":%q,"prompt":%q,"base64_images":["data:image/png;base64,%s"]}`, id, prompt, image)
}

func TestEditableFileTaskRequiresImagesForPPTAndPSD(t *testing.T) {
	server := New(adminTestConfig(t.TempDir()))
	t.Cleanup(func() { _ = server.Close() })
	for _, path := range []string{"/v1/ppt/generations", "/v1/psd/generations"} {
		response := editableTaskRequest(server, http.MethodPost, path, `{"prompt":"做一份材料"}`)
		if response.Code != 400 || !strings.Contains(response.Body.String(), "base64_images is empty") {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestEditableFileTaskRejectsFreeAndUnknownSubscriptions(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		account map[string]any
	}{
		{name: "free", account: imageAccountPolicyFixture("free", "free-token", "free", false, now)},
		{name: "unknown", account: imageAccountPolicyFixture("unknown", "unknown-token", "unknown", true, now)},
		{name: "stale", account: imageAccountPolicyFixture("stale", "stale-token", "plus", true, now.Add(-2*time.Hour))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEditableTaskFixture(t)
			fixture.ready.Store(true)
			server := newEditableTaskServer(t, fixture, test.account)
			response := editableTaskRequest(server, http.MethodPost, "/v1/ppt/generations", editableImageBody("reject-"+test.name, "做一份 PPT"))
			if response.Code != 202 {
				t.Fatal(response.Body.String())
			}
			task := waitEditableTask(t, server, "reject-"+test.name)
			if task["status"] != "error" {
				t.Fatalf("admitted ineligible account: %#v", task)
			}
			if fixture.submits.Load() != 0 {
				t.Fatal("ineligible account was submitted upstream")
			}
			if err := server.store.FlushAccounts(); err != nil {
				t.Fatal(err)
			}
			items, err := server.store.AccountList()
			if err != nil {
				t.Fatal(err)
			}
			if intValue(items[0]["success"]) != 0 || intValue(items[0]["fail"]) != 0 || items[0]["last_error_status"] != nil {
				t.Fatalf("ineligible account was punished: %#v", items[0])
			}
		})
	}
}

func TestEditableLocalErrorsDoNotPunishAccount(t *testing.T) {
	now := time.Now()
	t.Run("decode", func(t *testing.T) {
		fixture := newEditableTaskFixture(t)
		server := newEditableTaskServer(t, fixture, imageAccountPolicyFixture("paid", "paid-token", "plus", true, now))
		response := editableTaskRequest(server, http.MethodPost, "/v1/ppt/generations", `{"client_task_id":"bad-decode","prompt":"做一份 PPT","base64_images":["%%%"]}`)
		if response.Code != 202 {
			t.Fatal(response.Body.String())
		}
		task := waitEditableTask(t, server, "bad-decode")
		if task["status"] != "error" || fixture.submits.Load() != 0 {
			t.Fatalf("decode: %#v submits=%d", task, fixture.submits.Load())
		}
		if err := server.store.FlushAccounts(); err != nil {
			t.Fatal(err)
		}
		items, _ := server.store.AccountList()
		if intValue(items[0]["success"]) != 0 || intValue(items[0]["fail"]) != 0 || items[0]["last_error_status"] != nil || items[0]["cooldown_until"] != nil {
			t.Fatalf("decode punished account: %#v", items[0])
		}
	})
	t.Run("save after upstream success", func(t *testing.T) {
		fixture := newEditableTaskFixture(t)
		fixture.ready.Store(true)
		server := newEditableTaskServer(t, fixture, imageAccountPolicyFixture("paid", "paid-token", "plus", true, now))
		if err := os.WriteFile(filepath.Join(server.cfg.DataDir, "files"), []byte("blocked"), 0o600); err != nil {
			t.Fatal(err)
		}
		response := editableTaskRequest(server, http.MethodPost, "/v1/ppt/generations", editableImageBody("save-fail", "做一份 PPT"))
		if response.Code != 202 {
			t.Fatal(response.Body.String())
		}
		task := waitEditableTask(t, server, "save-fail")
		if task["status"] != "error" || fixture.submits.Load() != 1 {
			t.Fatalf("save: %#v submits=%d", task, fixture.submits.Load())
		}
		if err := server.store.FlushAccounts(); err != nil {
			t.Fatal(err)
		}
		items, _ := server.store.AccountList()
		if intValue(items[0]["success"]) != 1 || intValue(items[0]["fail"]) != 0 || items[0]["last_error_status"] != nil {
			t.Fatalf("local save was not settled as success: %#v", items[0])
		}
	})
}

func TestEditableFileTaskResumesOriginalAccountWithoutRepost(t *testing.T) {
	now := time.Now()
	fixture := newEditableTaskFixture(t)
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL = fixture.server.URL
	cfg.RequestTimeout = 4 * time.Second
	server := New(cfg)
	if _, _, _, err := server.store.AddAccounts(nil, []map[string]any{
		imageAccountPolicyFixture("origin", "original-at", "plus", true, now),
		imageAccountPolicyFixture("other", "other-at", "plus", true, now),
	}); err != nil {
		t.Fatal(err)
	}
	response := editableTaskRequest(server, http.MethodPost, "/v1/ppt/generations", editableImageBody("recoverable", "做一份 PPT"))
	if response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	waitEditableCheckpoint(t, cfg.DataDir)
	if fixture.submits.Load() != 1 {
		t.Fatalf("submits=%d", fixture.submits.Load())
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.ready.Store(true)
	fixture.expectedToken.Store("original-at")
	restarted := New(cfg)
	defer restarted.Close()
	task := waitEditableTask(t, restarted, "recoverable")
	if task["status"] != "success" || fixture.submits.Load() != 1 || fixture.wrongAccount.Load() {
		t.Fatalf("resume failed or regenerated: %#v submits=%d wrong=%v", task, fixture.submits.Load(), fixture.wrongAccount.Load())
	}
}

func TestEditableFileTaskWithoutCheckpointIsNotReplayed(t *testing.T) {
	cfg := adminTestConfig(t.TempDir())
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"tasks": []*editableFileTaskState{{
		ID: "old", TaskID: "old", Owner: "api", Status: "running", Kind: "ppt",
		CreatedAt: time.Now().UTC().Format(time.RFC3339), UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}}})
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "editable_file_tasks.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := newEditableTaskFixture(t)
	cfg.OpenAIBaseURL = fixture.server.URL
	server := New(cfg)
	defer server.Close()
	task := waitEditableTask(t, server, "old")
	if task["status"] != "error" || !strings.Contains(fmt.Sprint(task["error"]), "没有可续查检查点") {
		t.Fatalf("old task: %#v", task)
	}
	if fixture.submits.Load() != 0 {
		t.Fatal("old task was replayed")
	}
}

func TestEditableFileTaskDoesNotTrustEditablePlanField(t *testing.T) {
	now := time.Now()
	account := imageAccountPolicyFixture("free", "free-token", "free", false, now)
	account["type"] = "pro"
	account["plan_type"] = "pro"
	fixture := newEditableTaskFixture(t)
	fixture.ready.Store(true)
	server := newEditableTaskServer(t, fixture, account)
	response := editableTaskRequest(server, http.MethodPost, "/v1/ppt/generations", editableImageBody("trust-plan", "做一份 PPT"))
	if response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	task := waitEditableTask(t, server, "trust-plan")
	if task["status"] != "error" || fixture.submits.Load() != 0 {
		t.Fatalf("trusted editable plan: %#v submits=%d", task, fixture.submits.Load())
	}
}

func flushedAccount(t *testing.T, s *Server) map[string]any {
	t.Helper()
	if err := s.store.FlushAccounts(); err != nil {
		t.Fatal(err)
	}
	items, err := s.store.AccountList()
	if err != nil || len(items) == 0 {
		t.Fatalf("accounts: %v %#v", err, items)
	}
	return items[0]
}

func TestEditableFileTaskTimeoutIgnoresRequestTimeout(t *testing.T) {
	t.Setenv("GO_REQUEST_TIMEOUT_SECONDS", "180")
	loaded, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RequestTimeout != 180*time.Second {
		t.Fatalf("request timeout %v", loaded.RequestTimeout)
	}
	cfg := adminTestConfig(t.TempDir())
	cfg.RequestTimeout = loaded.RequestTimeout
	server := New(cfg)
	t.Cleanup(func() { _ = server.Close() })
	if got := server.editableTaskTimeout(); got != 25*time.Minute {
		t.Fatalf("RequestTimeout=%v shortened file task to %v", cfg.RequestTimeout, got)
	}
}

func TestEditableAcceptedPoll429DoesNotCountSuccess(t *testing.T) {
	now := time.Now()
	fixture := newEditableTaskFixture(t)
	fixture.rateLimited.Store(true)
	server := newEditableTaskServer(t, fixture, imageAccountPolicyFixture("paid", "paid-token", "plus", true, now))
	task := &editableFileTaskState{
		ID: "poll-429", TaskID: "poll-429", Owner: "api", Status: "running", Kind: "ppt",
		Prompt: "做一份 PPT", Images: []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG)},
		CreatedAt: time.Now().UTC().Format(time.RFC3339), UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	server.fileTaskMu.Lock()
	server.fileTasks[editableTaskKey(task.Owner, task.ID)] = task
	server.fileTaskMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	server.executeEditableFileTask(ctx, task)
	if task.Status != "error" {
		t.Fatalf("status=%s error=%s", task.Status, task.Error)
	}
	if fixture.submits.Load() != 1 {
		t.Fatalf("regenerated: submits=%d", fixture.submits.Load())
	}
	if fixture.polls.Load() < 1 {
		t.Fatal("did not poll after accept")
	}
	item := flushedAccount(t, server)
	if intValue(item["success"]) != 0 || intValue(item["fail"]) != 1 || intValue(item["last_error_status"]) != http.StatusTooManyRequests {
		t.Fatalf("429 settlement: %#v", item)
	}
	if item["cooldown_until"] == nil || strings.TrimSpace(fmt.Sprint(item["cooldown_until"])) == "" {
		t.Fatalf("missing cooldown: %#v", item)
	}
	if fmt.Sprint(item["last_error_kind"]) != "rate_limited" {
		t.Fatalf("kind=%v", item["last_error_kind"])
	}
}

func TestEditableCheckpointPersistFailureDoesNotCountFailure(t *testing.T) {
	now := time.Now()
	fixture := newEditableTaskFixture(t)
	fixture.holdSubmit = make(chan struct{})
	fixture.enteredSubmit = make(chan struct{}, 1)
	release := sync.OnceFunc(func() { close(fixture.holdSubmit) })
	t.Cleanup(release)
	server := newEditableTaskServer(t, fixture, imageAccountPolicyFixture("paid", "paid-token", "plus", true, now))
	response := editableTaskRequest(server, http.MethodPost, "/v1/ppt/generations", editableImageBody("ckpt-fail", "做一份 PPT"))
	if response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	select {
	case <-fixture.enteredSubmit:
	case <-time.After(3 * time.Second):
		t.Fatal("conversation not reached")
	}
	path := server.editableFileTasksPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	release()
	task := waitEditableTask(t, server, "ckpt-fail")
	if task["status"] != "error" || fixture.submits.Load() != 1 {
		t.Fatalf("%#v submits=%d", task, fixture.submits.Load())
	}
	item := flushedAccount(t, server)
	if intValue(item["success"]) != 1 || intValue(item["fail"]) != 0 || item["last_error_status"] != nil {
		t.Fatalf("checkpoint persist punished account: %#v", item)
	}
}
