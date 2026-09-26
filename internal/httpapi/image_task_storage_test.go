package httpapi

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageTaskCorruptStorageFailsClosed(t *testing.T) {
	cfg := adminTestConfig(t.TempDir())
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.DataDir, "image_tasks.json")
	original := []byte(`{"version":999,"tasks":[]}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	server := New(cfg)
	defer server.Close()
	response := imageTaskRequest(server, http.MethodPost, "/api/image-tasks/generations", "api-secret", strings.NewReader(`{"client_task_id":"blocked","prompt":"fixture"}`))
	if response.Code != 503 {
		t.Fatal("corrupt storage accepted a task")
	}
	health := imageTaskRequest(server, http.MethodGet, "/health", "api-secret", nil)
	if health.Code != 503 {
		t.Fatal("corrupt task storage reported healthy")
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, original) {
		t.Fatal("unknown task schema was overwritten")
	}
}

func TestImageTaskWriteFailureReleasesAdmission(t *testing.T) {
	cfg := adminTestConfig(t.TempDir())
	server := New(cfg)
	defer server.Close()
	if err := os.MkdirAll(server.imageTasksPath(), 0700); err != nil {
		t.Fatal(err)
	}
	response := imageTaskRequest(server, http.MethodPost, "/api/image-tasks/generations", "api-secret", strings.NewReader(`{"client_task_id":"failed","prompt":"fixture"}`))
	if response.Code != 503 {
		t.Fatal("failed persistence acknowledged a task")
	}
	if err := os.Remove(server.imageTasksPath()); err != nil {
		t.Fatal(err)
	}
	response = imageTaskRequest(server, http.MethodPost, "/api/image-tasks/generations", "api-secret", strings.NewReader(`{"client_task_id":"recovered","prompt":"fixture"}`))
	if response.Code != 202 {
		t.Fatalf("admission or storage did not recover: %d", response.Code)
	}
	_ = waitImageTask(t, server, "api-secret", "recovered")
	server.imageTaskMu.RLock()
	defer server.imageTaskMu.RUnlock()
	if server.imageTaskPending != 0 || server.imageTaskReserved != 0 || len(server.imageTasks) != 1 {
		t.Fatal("failed persistence leaked task state or admission capacity")
	}
}

func TestImageTaskOwnerCannotReadOrResumeAnotherOwnersTask(t *testing.T) {
	server := New(adminTestConfig(t.TempDir()))
	defer server.Close()
	_, otherKey, err := server.store.CreateKey("user", "other", server.cfg.AdminKey)
	if err != nil {
		t.Fatal(err)
	}
	response := imageTaskRequest(server, http.MethodPost, "/api/image-tasks/generations", "api-secret", strings.NewReader(`{"client_task_id":"private","prompt":"fixture"}`))
	if response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	_ = waitImageTask(t, server, "api-secret", "private")
	for _, action := range []struct{ method, path string }{{http.MethodGet, "/api/image-tasks/private"}, {http.MethodPost, "/api/image-tasks/private/resume-poll"}} {
		response = imageTaskRequest(server, action.method, action.path, otherKey, nil)
		if response.Code != 404 {
			t.Fatal("task crossed its owner boundary")
		}
	}
}
