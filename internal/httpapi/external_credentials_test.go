package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accountpolicy"
	"github.com/maiphucgiang/gpt2api/internal/accounts"
	"github.com/maiphucgiang/gpt2api/internal/protocol"
	"github.com/maiphucgiang/gpt2api/internal/store"
)

type externalCredentialFixture struct {
	s                      *Server
	old, next              string
	reads, oauth, business atomic.Int32
	sourceStatus           atomic.Int32
	plan                   atomic.Value
	gate                   chan struct{}
}

func newExternalCredentialFixture(t *testing.T, owner string, expiry time.Time) *externalCredentialFixture {
	t.Helper()
	f := &externalCredentialFixture{old: bpsTestToken("workspace", expiry), next: bpsTestToken("workspace", time.Now().Add(2*time.Hour))}
	f.plan.Store("plus")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/auth-files/download", "/api/v1/admin/accounts/data":
			f.reads.Add(1)
			if f.gate != nil {
				<-f.gate
			}
			if code := f.sourceStatus.Load(); code != 0 {
				http.Error(w, "source unavailable", int(code))
				return
			}
			credential := map[string]any{"type": "codex", "access_token": f.next, "refresh_token": "source-owned-rt", "account_id": "workspace"}
			if owner == "sub2api" {
				if r.Header.Get("x-api-key") != "source-key" || r.URL.Query().Get("ids") != "42" {
					t.Error("source authentication or remote id missing")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accounts": []any{map[string]any{"id": 42, "platform": "openai", "type": "oauth", "credentials": credential}}}})
			} else {
				if r.Header.Get("Authorization") != "Bearer source-key" || r.URL.Query().Get("name") != "42" {
					t.Error("CPA source binding missing")
				}
				_ = json.NewEncoder(w).Encode(credential)
			}
		case "/oauth/token":
			f.oauth.Add(1)
			http.Error(w, "must not rotate source RT", 500)
		case "/backend-api/me":
			if r.Header.Get("Authorization") == "Bearer "+f.old {
				http.Error(w, "expired", 401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user", "email": "source@example.test"})
		case "/backend-api/accounts/check/v4-2023-04-27":
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": map[string]any{"workspace": map[string]any{"account": map[string]any{"account_id": "workspace", "plan_type": f.plan.Load()}, "entitlement": map[string]any{"has_active_subscription": f.plan.Load() == "plus", "expires_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)}}}})
		case "/backend-api/conversation/init":
			_ = json.NewEncoder(w).Encode(map[string]any{"limits_progress": []any{map[string]any{"feature_name": "image_gen", "remaining": 9}}})
		case "/basispoints/api/responses":
			f.business.Add(1)
			if r.Header.Get("Authorization") == "Bearer "+f.old {
				http.Error(w, "expired", 401)
				return
			}
			bpsFixture(w, []any{bpsFixtureMessage()})
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	cfg := adminTestConfig(t.TempDir())
	cfg.OpenAIBaseURL, cfg.OpenAIOAuthURL, cfg.BPSBaseURL, cfg.BPSEnabled = upstream.URL, upstream.URL+"/oauth/token", upstream.URL, true
	f.s = New(cfg)
	t.Cleanup(func() { _ = f.s.Close() })
	if err := f.s.external.saveCPAList([]externalCPAPool{{ID: "source", BaseURL: upstream.URL, SecretKey: "source-key"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.external.saveSubList([]externalSubServer{{ID: "source", BaseURL: upstream.URL, APIKey: "source-key", VerifyTLS: true}}); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := f.s.store.AddAccounts(nil, []map[string]any{{"id": "external", "access_token": f.old, "refresh_token": "legacy-rt", "account_id": "workspace", "source_type": "codex", "credential_owner": owner, "credential_source_id": "source", "credential_remote_id": "42", "status": "正常", "enabled": true, "success": 8, "fail": 3, "subscription_active": true, "subscription_plan": "plus", "subscription_workspace_id": "workspace", "subscription_checked_at": time.Now().UTC().Format(time.RFC3339)}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestExternalRefreshEntrypointsNeverRotateRT(t *testing.T) {
	for _, owner := range []string{"cpa", "sub2api"} {
		for _, mode := range []string{"expiry", "manual", "401", "scheduled"} {
			t.Run(owner+"/"+mode, func(t *testing.T) {
				expiry := time.Now().Add(time.Hour)
				if mode == "expiry" {
					expiry = time.Now().Add(-time.Hour)
				}
				f := newExternalCredentialFixture(t, owner, expiry)
				if mode == "scheduled" {
					rows, _ := f.s.store.AccountList()
					if got := f.s.survivalProbe(rows[0], true); got != "plus" {
						t.Fatal("scheduled refresh failed", got)
					}
				} else if _, err := f.s.refreshAccountSnapshotMode(context.Background(), "external", mode == "manual"); err != nil {
					t.Fatal(err)
				}
				rows, err := store.New(f.s.cfg.AccountsPath, "", "").AccountList()
				if err != nil || len(rows) != 1 {
					t.Fatal("account not persisted", err)
				}
				x := rows[0]
				if x["id"] != "external" || x["access_token"] != f.next || x["refresh_token"] != nil || intValue(x["success"]) != 8 || intValue(x["fail"]) != 3 || !accountpolicy.SubscriptionEligible(x, time.Now()) {
					t.Fatal("sync lost identity, counters or subscription evidence")
				}
				if f.oauth.Load() != 0 || f.reads.Load() != 1 {
					t.Fatal("unexpected OAuth rotation or duplicate source reads", f.oauth.Load(), f.reads.Load())
				}
			})
		}
	}
}

func TestExternalOutageBacksOffWithoutDeletingOrCountingAccount(t *testing.T) {
	f := newExternalCredentialFixture(t, "cpa", time.Now().Add(-time.Hour))
	f.sourceStatus.Store(503)
	for i := 0; i < 2; i++ {
		rows, _ := f.s.store.AccountList()
		a := accounts.Account{Token: accountToken(rows[0]), Fields: rows[0]}
		err := f.s.runAccountBusiness(context.Background(), &a, func() error { t.Error("expired token used"); return nil }, func(error) bool { return true })
		if !errors.Is(err, accountpolicy.ErrCredentialPending) {
			t.Fatal("source outage not classified", err)
		}
		f.s.accountPool.Feedback(a, upstreamStatus(err), err)
	}
	rows, _ := f.s.store.AccountList()
	if len(rows) != 1 || rows[0]["access_token"] != f.old || intValue(rows[0]["fail"]) != 3 || rows[0]["credential_sync_status"] != "pending" || f.reads.Load() != 1 || f.oauth.Load() != 0 {
		t.Fatal("source failure altered credentials/counters or ignored backoff")
	}
	rows[0]["status"] = "异常"
	if accountAutoRemoveInvalid(rows[0]) {
		t.Fatal("managed account would be auto-deleted")
	}
}

func TestExternal401RetriesOnceWithoutResubmittingAcceptedWork(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{true: "accepted", false: "unauthorized"}[accepted], func(t *testing.T) {
			f := newExternalCredentialFixture(t, "cpa", time.Now().Add(time.Hour))
			rows, _ := f.s.store.AccountList()
			a := accounts.Account{Token: f.old, Fields: rows[0]}
			calls := 0
			err := f.s.runAccountBusiness(context.Background(), &a, func() error {
				calls++
				if a.Token == f.old {
					return &protocol.UpstreamError{Status: 401, Message: "unauthorized"}
				}
				return nil
			}, func(error) bool { return !accepted })
			f.s.accountPool.Feedback(a, upstreamStatusOrOK(err), err)
			if accepted {
				if calls != 1 || f.reads.Load() != 0 {
					t.Fatal("accepted or uncertain work resubmitted")
				}
			} else {
				if err != nil || calls != 2 || f.reads.Load() != 1 {
					t.Fatal("401 synchronization failed", err)
				}
				rows, _ = f.s.store.AccountList()
				if intValue(rows[0]["success"]) != 9 || intValue(rows[0]["fail"]) != 4 {
					t.Fatal("attempts not settled once")
				}
			}
			if f.oauth.Load() != 0 {
				t.Fatal("source RT rotated")
			}
		})
	}
}

func TestExternalConcurrent401SharesSourceSynchronization(t *testing.T) {
	f := newExternalCredentialFixture(t, "cpa", time.Now().Add(time.Hour))
	f.gate = make(chan struct{})
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.s.refreshAccountSnapshotExpected(context.Background(), "external", true, f.old)
			errs <- err
		}()
	}
	close(start)
	close(f.gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.reads.Load() != 1 || f.oauth.Load() != 0 {
		t.Fatal("concurrent synchronization not coalesced", f.reads.Load())
	}
}

func TestBPSExternal401RechecksSubscriptionBeforeRetry(t *testing.T) {
	for _, plan := range []string{"plus", "free"} {
		t.Run(plan, func(t *testing.T) {
			f := newExternalCredentialFixture(t, "cpa", time.Now().Add(time.Hour))
			f.plan.Store(plan)
			w := bpsRequest(f.s, "/v1/responses", f.s.cfg.APIKey, map[string]any{"model": "bsg/gpt-6-astra", "input": "hello"})
			if plan == "free" {
				if w.Code != 403 || !strings.Contains(w.Body.String(), "bps_subscription_required") || f.business.Load() != 1 {
					t.Fatal("downgraded account retried BPS", w.Code, w.Body.String())
				}
			} else if w.Code != 200 || f.business.Load() != 2 {
				t.Fatal(w.Code, w.Body.String())
			}
			if f.oauth.Load() != 0 || f.reads.Load() != 1 {
				t.Fatal("BPS did not delegate credential renewal")
			}
		})
	}
}

func TestBPSRejectsFreeAndUnknownForEveryProtocol(t *testing.T) {
	for _, plan := range []string{"free", "", "unrecognized"} {
		f := newExternalCredentialFixture(t, "cpa", time.Now().Add(time.Hour))
		rows, _ := f.s.store.AccountList()
		_, err := f.s.store.UpdateAccountIfCredentialsMatch(store.CredentialsOf(rows[0]), map[string]any{"type": "pro", "subscription_plan": plan})
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
			w := bpsRequest(f.s, path, f.s.cfg.APIKey, map[string]any{"model": "bsg/gpt-6-astra", "input": "hello", "messages": []any{map[string]any{"role": "user", "content": "hello"}}})
			if w.Code != 503 {
				t.Fatal("ineligible subscription admitted", plan, path, w.Code, w.Body.String())
			}
		}
		if f.business.Load() != 0 || f.oauth.Load() != 0 {
			t.Fatal("ineligible account reached upstream")
		}
	}
}
