package accountpolicy

import (
	"testing"
	"time"
)

func TestCredentialOwnershipAndExpiry(t *testing.T) {
	for _, source := range []string{"codex", "cpa_json", "remote_cpa", "sub2api"} {
		if !External(map[string]any{"source_type": source}) {
			t.Errorf("legacy %s must await source binding", source)
		}
	}
	if External(map[string]any{"source_type": "oauth_login"}) {
		t.Fatal("local OAuth must retain ownership")
	}
	when := time.Now().Add(time.Hour).Truncate(time.Second)
	for _, value := range []any{when.Format(time.RFC3339), when.Unix(), float64(when.Unix()), when.UnixMilli()} {
		if got := Expiry(map[string]any{"expires_at": value}); !got.Equal(when) {
			t.Fatalf("expiry %v: %v", value, got)
		}
	}
}

func TestSubscriptionRequiresFreshWorkspaceEvidence(t *testing.T) {
	now := time.Now().UTC()
	base := map[string]any{"type": "free", "account_id": "workspace", "subscription_plan": "plus", "subscription_active": true, "subscription_workspace_id": "workspace", "subscription_checked_at": now.Format(time.RFC3339)}
	if !SubscriptionEligible(base, now) {
		t.Fatal("upstream paid subscription should not depend on editable type")
	}
	for name, change := range map[string]map[string]any{
		"free":            {"type": "pro", "subscription_plan": "free"},
		"unknown":         {"subscription_plan": "unknown"},
		"inactive":        {"subscription_active": false},
		"stale":           {"subscription_checked_at": now.Add(-time.Hour).Format(time.RFC3339)},
		"future":          {"subscription_checked_at": now.Add(time.Hour).Format(time.RFC3339)},
		"other workspace": {"subscription_workspace_id": "other"},
		"expired":         {"subscription_expires_at": now.Add(-time.Second).Format(time.RFC3339)},
		"invalid expiry":  {"subscription_expires_at": "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			item := make(map[string]any)
			for k, v := range base {
				item[k] = v
			}
			for k, v := range change {
				item[k] = v
			}
			if SubscriptionEligible(item, now) {
				t.Fatal("unverified subscription admitted")
			}
		})
	}
}
