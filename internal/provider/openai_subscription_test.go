package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accountpolicy"
)

func TestExternalCredentialsCannotReachOAuthThroughProvider(t *testing.T) {
	var calls atomic.Int32
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			calls.Add(1)
		}
		http.Error(w, "unauthorized", 401)
	}))
	defer u.Close()
	client := NewOpenAIAccountClient(u.URL, u.URL+"/oauth/token", u.Client(), nil)
	for _, owner := range []string{"cpa", "sub2api", "external_unbound", ""} {
		a := map[string]any{"access_token": "old", "refresh_token": "owned-upstream", "source_type": "codex", "credential_owner": owner}
		if _, err := client.RefreshAccount(context.Background(), a); err == nil {
			t.Fatal("401 unexpectedly succeeded")
		}
		if _, err := client.RefreshAccessToken(context.Background(), a); err == nil {
			t.Fatal("external forced refresh accepted")
		}
		if _, err := client.refreshOAuth(context.Background(), "owned-upstream", a); err == nil {
			t.Fatal("direct external rotation accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("externally managed RT rotated")
	}
}

func TestSubscriptionUsesSelectedWorkspaceNotPaidDefault(t *testing.T) {
	for _, selected := range []string{"free-space", "paid-space", "missing-space"} {
		t.Run(selected, func(t *testing.T) {
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/backend-api/me":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "user"})
				case "/backend-api/conversation/init":
					_ = json.NewEncoder(w).Encode(map[string]any{"limits_progress": []any{}})
				case "/backend-api/accounts/check/v4-2023-04-27":
					_ = json.NewEncoder(w).Encode(map[string]any{"accounts": map[string]any{
						"default":    map[string]any{"account": map[string]any{"account_id": "paid-space", "plan_type": "pro"}, "entitlement": map[string]any{"has_active_subscription": true}},
						"free-space": map[string]any{"account": map[string]any{"account_id": "free-space", "plan_type": "free"}},
					}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer u.Close()
			a := map[string]any{"access_token": "token", "account_id": selected, "type": "pro"}
			result, err := NewOpenAIAccountClient(u.URL, u.URL, u.Client(), nil).RefreshAccount(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range result.Fields {
				a[k] = v
			}
			if accountpolicy.SubscriptionEligible(a, time.Now()) != (selected == "paid-space") {
				t.Fatal("subscription belongs to the wrong workspace")
			}
		})
	}
}

func TestSubscriptionRequiresActiveEntitlement(t *testing.T) {
	now := time.Now().UTC()
	for _, entitlement := range []map[string]any{nil, {"has_active_subscription": false}, {"has_active_subscription": true, "expires_at": now.Add(-time.Second).Format(time.RFC3339)}} {
		a := map[string]any{"account_id": "workspace", "type": "pro"}
		for k, v := range subscriptionFields(map[string]any{"plan_type": "pro", "entitlement": entitlement}, a) {
			a[k] = v
		}
		if accountpolicy.SubscriptionEligible(a, now) {
			t.Fatal("missing, inactive or expired entitlement admitted")
		}
	}
}
