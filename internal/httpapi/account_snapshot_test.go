package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func writeAccountSnapshotFixture(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/backend-api/me":
		_ = json.NewEncoder(w).Encode(map[string]any{"email": "test@example.test", "id": "test-user"})
	case "/backend-api/conversation/init":
		_ = json.NewEncoder(w).Encode(map[string]any{"default_model_slug": "gpt-5", "limits_progress": []any{}})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"accounts": map[string]any{"default": map[string]any{"account": map[string]any{"plan_type": "plus"}}}})
	}
}

func TestAccountSnapshotCoalescesAndHandlesRemoval(t *testing.T) {
	for _, remove := range []bool{false, true} {
		name := "cancelled_waiter"
		if remove {
			name = "removed_during_refresh"
		}
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}, 1), make(chan struct{})
			var count atomic.Int32
			var unblockOnce sync.Once
			unblock := func() { unblockOnce.Do(func() { close(release) }) }
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/backend-api/me" {
					count.Add(1)
					entered <- struct{}{}
					<-release
				}
				writeAccountSnapshotFixture(w, r)
			}))
			defer upstream.Close()
			defer unblock()
			cfg := adminTestConfig(t.TempDir())
			cfg.OpenAIBaseURL = upstream.URL
			server := New(cfg)
			t.Cleanup(func() { _ = server.Close() })
			_, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"access_token": "snapshot-token", "source_type": "chatgpt_web"}})
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				_, err := server.refreshAccountSnapshot(server.backgroundContext, "snapshot-token")
				result <- err
			}()
			<-entered
			if remove {
				if _, _, err := server.store.DeleteAccounts([]string{"snapshot-token"}); err != nil {
					t.Fatal(err)
				}
			} else {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := server.refreshAccountSnapshot(ctx, "snapshot-token"); !errors.Is(err, context.Canceled) {
					t.Fatalf("waiter error: %v", err)
				}
			}
			unblock()
			err = <-result
			if (err != nil) != remove {
				t.Fatalf("refresh error: %v; removed=%v", err, remove)
			}
			if count.Load() != 1 {
				t.Fatal("overlapping probes were duplicated")
			}
		})
	}
}

func TestSurvivalHonorsCredentialRotationSetting(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		var rotations atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/oauth/token" {
				rotations.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "id_token": "new-id"})
				return
			}
			writeAccountSnapshotFixture(w, r)
		}))
		cfg := adminTestConfig(t.TempDir())
		cfg.OpenAIBaseURL, cfg.OpenAIOAuthURL = upstream.URL, upstream.URL+"/oauth/token"
		server := New(cfg)
		_, _, items, err := server.store.AddAccounts(nil, []map[string]any{{"access_token": "existing-access", "refresh_token": "existing-refresh", "source_type": "oauth_login"}})
		if err != nil {
			t.Fatal(err)
		}
		if status := server.survivalProbe(items[0], rotate); status != "plus" {
			t.Fatalf("probe status: %s", status)
		}
		if got := rotations.Load() > 0; got != rotate {
			t.Fatalf("rotation=%v, wanted %v", got, rotate)
		}
		_ = server.Close()
		upstream.Close()
	}
}

func TestSnapshotRefreshesTheSelectedWorkspaceMember(t *testing.T) {
	var wrongMember atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer member-b" {
			wrongMember.Store(true)
		}
		writeAccountSnapshotFixture(w, r)
	}))
	defer upstream.Close()
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL = upstream.URL
	server := New(cfg)
	t.Cleanup(func() { _ = server.Close() })
	_, _, items, err := server.store.AddAccounts(nil, []map[string]any{
		{"access_token": "member-a", "source_type": "chatgpt_web", "account_id": "shared-workspace", "email": "a@example.test"},
		{"access_token": "member-b", "source_type": "chatgpt_web", "account_id": "shared-workspace", "email": "b@example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := server.refreshAccountSnapshot(context.Background(), "member-b")
	if err != nil {
		t.Fatal(err)
	}
	if wrongMember.Load() || updated["id"] != items[1]["id"] {
		t.Fatal("refreshed the wrong workspace member")
	}
}

func TestUnsupportedCredentialCannotRefreshOrExportAsCodex(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL = upstream.URL
	server := New(cfg)
	t.Cleanup(func() { _ = server.Close() })
	credential := map[string]any{"access_token": "foreign-access", "refresh_token": "foreign-refresh", "id_token": "foreign-id", "credential_provider": "unsupported"}
	_, _, _, err := server.store.AddAccounts(nil, []map[string]any{credential})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.refreshAccountSnapshot(context.Background(), "foreign-access"); err == nil {
		t.Fatal("foreign account refreshed")
	}
	if _, ok := buildAccountExportItem(credential); ok {
		t.Fatal("foreign account exported as Codex")
	}
	if calls.Load() != 0 {
		t.Fatal("foreign credential sent to GPT upstream")
	}
}
