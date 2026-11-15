package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"
)

func imageAccountPolicyFixture(id, token, plan string, active bool, checked time.Time) map[string]any {
	workspace := id + "-workspace"
	account := map[string]any{
		"id":                        id,
		"access_token":              token,
		"account_id":                workspace,
		"source_type":               "chatgpt_web",
		"pool":                      "basic",
		"enabled":                   true,
		"status":                    "正常",
		"subscription_plan":         plan,
		"subscription_active":       active,
		"subscription_workspace_id": workspace,
		"subscription_checked_at":   checked.UTC().Format(time.RFC3339),
	}
	if active {
		account["subscription_expires_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	}
	return account
}

func newImageAccountPolicyServer(t *testing.T, items ...map[string]any) *Server {
	t.Helper()
	cfg := adminTestConfig(t.TempDir())
	cfg.ImageAccountLimit = 1
	server := New(cfg)
	t.Cleanup(func() { _ = server.Close() })
	if _, _, _, err := server.store.AddAccounts(nil, items); err != nil {
		t.Fatal(err)
	}
	return server
}

func TestHighResolutionImageAccountPrefersPaid(t *testing.T) {
	now := time.Now()
	server := newImageAccountPolicyServer(t,
		imageAccountPolicyFixture("free", "free-token", "free", false, now),
		imageAccountPolicyFixture("paid", "paid-token", "plus", true, now),
	)
	for _, size := range []string{"1921x1088", "2048x2048", "3840x2160"} {
		lease, err := server.reserveOpenAIImageAccount(context.Background(), nil, size)
		if err != nil {
			t.Fatalf("reserve %s: %v", size, err)
		}
		if lease.Account.Token != "paid-token" {
			t.Fatalf("reserve %s selected %q", size, lease.Account.Token)
		}
		server.accountPool.Release(lease)
	}
}

func TestStandardResolutionImageAccountAllowsFree(t *testing.T) {
	server := newImageAccountPolicyServer(t,
		imageAccountPolicyFixture("free", "free-token", "free", false, time.Now()),
	)
	for _, size := range []string{"", "auto", "1024x1024", "1920x1088"} {
		lease, err := server.reserveOpenAIImageAccount(context.Background(), nil, size)
		if err != nil {
			t.Fatalf("reserve %s: %v", size, err)
		}
		if lease.Account.Token != "free-token" {
			t.Fatalf("reserve %s selected %q", size, lease.Account.Token)
		}
		server.accountPool.Release(lease)
	}
}

func TestHighResolutionImageAccountFallsBackToFree(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		items    []map[string]any
		excluded map[string]bool
	}{
		{
			name:  "no paid account",
			items: []map[string]any{imageAccountPolicyFixture("free", "free-token", "free", false, now)},
		},
		{
			name: "paid subscription is stale",
			items: []map[string]any{
				imageAccountPolicyFixture("free", "free-token", "free", false, now),
				imageAccountPolicyFixture("paid", "paid-token", "plus", true, now.Add(-2*time.Hour)),
			},
		},
		{
			name: "paid attempt was excluded",
			items: []map[string]any{
				imageAccountPolicyFixture("free", "free-token", "free", false, now),
				imageAccountPolicyFixture("paid", "paid-token", "plus", true, now),
			},
			excluded: map[string]bool{"paid-token": true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newImageAccountPolicyServer(t, test.items...)
			lease, err := server.reserveOpenAIImageAccount(context.Background(), test.excluded, "2048x2048")
			if err != nil {
				t.Fatal(err)
			}
			defer server.accountPool.Release(lease)
			if lease.Account.Token != "free-token" {
				t.Fatalf("selected %q", lease.Account.Token)
			}
		})
	}
}

func TestHighResolutionImageAccountWaitsForBusyPaidAccount(t *testing.T) {
	now := time.Now()
	server := newImageAccountPolicyServer(t,
		imageAccountPolicyFixture("free", "free-token", "free", false, now),
		imageAccountPolicyFixture("paid", "paid-token", "plus", true, now),
	)
	held, err := server.reserveOpenAIImageAccount(context.Background(), nil, "2048x2048")
	if err != nil {
		t.Fatal(err)
	}
	defer server.accountPool.Release(held)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	lease, err := server.reserveOpenAIImageAccount(ctx, nil, "2048x2048")
	if lease != nil {
		server.accountPool.Release(lease)
		t.Fatal("busy paid account immediately fell back to free")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected wait deadline, got %v", err)
	}
}
