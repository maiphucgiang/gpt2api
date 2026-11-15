package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSurvivalPersistsQuotaWithoutRefreshToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/backend-api/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "quota@example.test", "id": "quota-user"})
		case "/backend-api/conversation/init":
			_ = json.NewEncoder(w).Encode(map[string]any{"limits_progress": []any{map[string]any{"feature_name": "image_gen", "remaining": 7, "reset_after": "2030-01-01T00:00:00Z"}}})
		case "/backend-api/accounts/check/v4-2023-04-27":
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": map[string]any{"default": map[string]any{"account": map[string]any{"plan_type": "prolite"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL = upstream.URL
	server := New(cfg)
	_, _, items, err := server.store.AddAccounts(nil, []map[string]any{{"access_token": "quota-token", "source_type": "chatgpt_web", "quota": 0, "status": "限流"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := server.survivalProbe(items[0], true); got != "prolite" {
		t.Fatalf("plan=%s", got)
	}
	items, err = server.store.AccountList()
	if err != nil {
		t.Fatal(err)
	}
	item := items[0]
	if intValue(item["quota"]) != 7 || item["status"] != "正常" || item["type"] != "prolite" || item["restore_at"] != "2030-01-01T00:00:00Z" {
		t.Fatalf("quota snapshot not persisted: quota=%v status=%v plan=%v reset=%v", item["quota"], item["status"], item["type"], item["restore_at"])
	}
	if !isHealthyOpenAIPlan("prolite") {
		t.Fatal("prolite must not be reported as unhealthy")
	}
}

func TestCPAOnlyImportsCodexAndDelegatesRefresh(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v0/management/auth-files" {
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{map[string]any{"name": "gpt.json", "type": "codex"}, map[string]any{"name": "foreign.json", "provider": "claude"}}})
			return
		}
		if r.URL.Query().Get("name") == "foreign.json" {
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "claude", "access_token": "foreign-token"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "codex", "access_token": "gpt-token", "refresh_token": "rt-test", "id_token": "id-test", "account_id": "account-test", "email": "gpt@example.test", "expired": "2030-01-01T00:00:00Z"})
	}))
	defer upstream.Close()
	server := New(adminTestConfig(t.TempDir()))
	pool := externalCPAPool{ID: "test-cpa", BaseURL: upstream.URL}
	if err := server.external.saveCPAList([]externalCPAPool{pool}); err != nil {
		t.Fatal(err)
	}
	files, err := server.cpaRemoteFiles(pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0]["name"] != "gpt.json" {
		t.Errorf("unsupported providers visible: %v", files)
	}
	job := jobFor(2)
	server.runCPAImport(pool, []string{"foreign.json", "gpt.json"}, job)
	items, err := server.store.AccountList()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("imported %d accounts, want only codex", len(items))
	}
	item := items[0]
	if item["access_token"] != "gpt-token" || item["refresh_token"] != nil || item["id_token"] != "id-test" || item["account_id"] != "account-test" || item["source_type"] != "codex" || item["credential_owner"] != "cpa" || item["credential_source_id"] != "test-cpa" || item["credential_remote_id"] != "gpt.json" {
		t.Fatal("credential ownership lost or refresh token imported")
	}
}
