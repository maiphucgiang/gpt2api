package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/store"
)

func TestBPS401RotatesOnceAndUsesCheckpointedCredentials(t *testing.T) {
	oldToken := bpsTestToken("old-workspace", time.Now().Add(time.Hour))
	newToken := bpsTestToken("new-workspace", time.Now().Add(2*time.Hour))
	var bpsCalls, oauthCalls atomic.Int32
	cfg := adminTestConfig(t.TempDir())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/basispoints/api/responses":
			bpsCalls.Add(1)
			if r.Header.Get("Authorization") == "Bearer "+oldToken {
				http.Error(w, "expired", 401)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+newToken || r.Header.Get("Chatgpt-Account-Id") != "new-workspace" {
				t.Error("stale BPS credentials or workspace")
			}
			disk := store.New(cfg.AccountsPath, "", "")
			rows, _ := disk.AccountList()
			if len(rows) != 1 || rows[0]["access_token"] != newToken || rows[0]["refresh_token"] != "rotated-rt" {
				t.Error("new tokens not checkpointed before BPS retry")
			}
			bpsFixture(w, []any{bpsFixtureMessage()})
		case "/oauth/token":
			oauthCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": newToken, "refresh_token": "rotated-rt"})
		case "/backend-api/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user", "email": "fixture@example.test"})
		case "/backend-api/conversation/init":
			_ = json.NewEncoder(w).Encode(map[string]any{"limits_progress": []any{}})
		case "/backend-api/accounts/check/v4-2023-04-27":
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": map[string]any{"new-workspace": map[string]any{"account": map[string]any{"plan_type": "plus"}, "entitlement": map[string]any{"has_active_subscription": true}}}})
		default:
			t.Error("unexpected URL", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	cfg.BPSEnabled = true
	cfg.BPSBaseURL = upstream.URL
	cfg.OpenAIBaseURL = upstream.URL
	cfg.OpenAIOAuthURL = upstream.URL + "/oauth/token"
	s := New(cfg)
	defer s.Close()
	_, _, _, err := s.store.AddAccounts(nil, []map[string]any{{"id": "account", "source_type": "oauth_login", "access_token": oldToken, "refresh_token": "old-rt", "enabled": true, "status": "正常", "success": 0, "fail": 0, "subscription_active": true, "subscription_plan": "plus", "subscription_workspace_id": "old-workspace", "subscription_checked_at": time.Now().UTC().Format(time.RFC3339)}})
	if err != nil {
		t.Fatal(err)
	}
	w := bpsRequest(s, "/v1/responses", cfg.APIKey, map[string]any{"model": "bsg/gpt-6-astra", "input": "hello"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "OK") || bpsCalls.Load() != 2 || oauthCalls.Load() != 1 {
		t.Fatal(w.Code, w.Body.String(), bpsCalls.Load(), oauthCalls.Load())
	}
	rows, _ := s.store.AccountList()
	if intValue(rows[0]["success"]) != 1 || intValue(rows[0]["fail"]) != 1 {
		t.Fatal("business attempts not settled exactly once")
	}
	// A delayed 401 from the previous credential version must reuse the checkpoint.
	updated, err := s.refreshAccountSnapshotExpected(context.Background(), "account", true, oldToken)
	if err != nil || updated["access_token"] != newToken || oauthCalls.Load() != 1 {
		t.Fatal("delayed unauthorized response rotated credentials again", err, oauthCalls.Load())
	}
}
