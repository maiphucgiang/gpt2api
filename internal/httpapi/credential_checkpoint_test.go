package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/maiphucgiang/gpt2api/internal/store"
)

func TestOAuthCheckpointPrecedesFailingProfileProbe(t *testing.T) {
	cfg := adminTestConfig(t.TempDir())
	var checkpointed atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "new-at", "refresh_token": "new-rt", "id_token": "new-id"})
			return
		}
		raw, _ := os.ReadFile(cfg.AccountsPath)
		var rows []map[string]any
		if json.Unmarshal(raw, &rows) == nil && len(rows) == 1 {
			checkpointed.Store(rows[0]["access_token"] == "new-at" && rows[0]["refresh_token"] == "new-rt")
		}
		http.Error(w, "temporary profile failure", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	cfg.OpenAIBaseURL, cfg.OpenAIOAuthURL = upstream.URL, upstream.URL+"/oauth/token"
	server := New(cfg)
	defer server.Close()
	_, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"id": "account", "source_type": "oauth_login", "access_token": "old-at", "refresh_token": "old-rt", "id_token": "old-id", "quota": 17, "success": 9, "status": "正常"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.refreshAccountSnapshotMode(context.Background(), "account", true); err == nil {
		t.Fatal("expected profile failure")
	}
	if !checkpointed.Load() {
		t.Fatal("rotated credentials were not persisted before the profile request")
	}
	disk := store.New(cfg.AccountsPath, "", "")
	rows, err := disk.AccountList()
	if err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if row["access_token"] != "new-at" || row["refresh_token"] != "new-rt" || row["id_token"] != "new-id" {
		t.Fatal("rotation lost after profile failure")
	}
	if intValue(row["quota"]) != 17 || intValue(row["success"]) != 9 {
		t.Fatal("probe failure changed quota or business statistics")
	}
	if row["quota_refresh_retry_at"] == nil {
		t.Fatal("probe retry missing")
	}
}

func TestCancellationAfterOAuthKeepsCheckpoint(t *testing.T) {
	entered := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "new-at", "refresh_token": "new-rt"})
			return
		}
		close(entered)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL, cfg.OpenAIOAuthURL = upstream.URL, upstream.URL+"/oauth/token"
	server := New(cfg)
	defer server.Close()
	_, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"id": "account", "source_type": "oauth_login", "access_token": "old-at", "refresh_token": "old-rt", "quota": 17}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := server.refreshAccountSnapshotMode(ctx, "account", true); done <- err }()
	<-entered
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
	rows, err := store.New(cfg.AccountsPath, "", "").AccountList()
	if err != nil || rows[0]["refresh_token"] != "new-rt" || intValue(rows[0]["quota"]) != 17 {
		t.Fatal("cancellation lost durable credentials or quota")
	}
}

func TestCredentialChangesRejectStaleRefreshResults(t *testing.T) {
	for _, phase := range []string{"oauth", "profile"} {
		t.Run(phase, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var profiles atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					if phase == "oauth" {
						close(entered)
						<-release
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "rotated-at", "refresh_token": "rotated-rt", "id_token": "rotated-id"})
					return
				}
				if r.URL.Path == "/backend-api/me" {
					profiles.Add(1)
					if phase == "profile" {
						close(entered)
						<-release
					}
				}
				writeAccountSnapshotFixture(w, r)
			}))
			defer upstream.Close()
			defer unblock()
			cfg := adminTestConfig(t.TempDir())
			cfg.OpenAIBaseURL, cfg.OpenAIOAuthURL = upstream.URL, upstream.URL+"/oauth/token"
			server := New(cfg)
			defer server.Close()
			_, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"id": "account", "source_type": "oauth_login", "access_token": "old-at", "refresh_token": "old-rt", "quota": 17}})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := server.refreshAccountSnapshotMode(context.Background(), "account", true)
				done <- err
			}()
			<-entered
			account, err := server.accountByRef("account")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := server.store.UpdateAccount(accountToken(account), map[string]any{"refresh_token": "operator-rt"}); err != nil {
				t.Fatal(err)
			}
			unblock()
			if err := <-done; err == nil {
				t.Fatal("stale refresh accepted")
			}
			account, err = server.accountByRef("account")
			if err != nil {
				t.Fatal(err)
			}
			if account["refresh_token"] != "operator-rt" || intValue(account["quota"]) != 17 {
				t.Fatal("stale refresh overwrote current account")
			}
			if account["last_quota_refresh_error"] != nil {
				t.Fatal("stale error attached to newer credentials")
			}
			if phase == "oauth" && profiles.Load() != 0 {
				t.Fatal("profile requested after checkpoint conflict")
			}
		})
	}
}

func TestOAuthCheckpointWriteFailureKeepsRotatedCredentialsForRecovery(t *testing.T) {
	cfg := adminTestConfig(t.TempDir())
	var profiles atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "new-at", "refresh_token": "new-rt"})
			return
		}
		profiles.Add(1)
		writeAccountSnapshotFixture(w, r)
	}))
	defer upstream.Close()
	cfg.OpenAIBaseURL, cfg.OpenAIOAuthURL = upstream.URL, upstream.URL+"/oauth/token"
	server := New(cfg)
	defer server.Close()
	if _, _, _, err := server.store.AddAccounts(nil, []map[string]any{{"id": "account", "source_type": "oauth_login", "access_token": "old-at", "refresh_token": "old-rt", "quota": 17}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.AccountsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg.AccountsPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := server.refreshAccountSnapshotMode(context.Background(), "account", true); err == nil {
		t.Fatal("checkpoint failure must not report success")
	}
	if profiles.Load() != 0 {
		t.Fatal("profile request continued without a durable checkpoint")
	}
	account, err := server.accountByRef("account")
	if err != nil || account["refresh_token"] != "new-rt" {
		t.Fatal("rotated refresh token was discarded after storage failure")
	}
	if err := os.Remove(cfg.AccountsPath); err != nil {
		t.Fatal(err)
	}
	if err := server.store.FlushAccounts(); err != nil {
		t.Fatal(err)
	}
	rows, err := store.New(cfg.AccountsPath, "", "").AccountList()
	if err != nil || rows[0]["refresh_token"] != "new-rt" || intValue(rows[0]["quota"]) != 17 {
		t.Fatal("storage recovery did not preserve rotated credentials and quota")
	}
}
