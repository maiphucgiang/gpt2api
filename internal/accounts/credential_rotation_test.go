package accounts

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/store"
)

func TestRotationKeepsInflightLimitAndRejectsStaleHealthFeedback(t *testing.T) {
	repo := store.New(filepath.Join(t.TempDir(), "accounts.json"), "", "")
	_, _, _, err := repo.AddAccounts(nil, []map[string]any{{"id": "account", "access_token": "old-at", "status": "正常", "quota": 10}})
	if err != nil {
		t.Fatal(err)
	}
	pool := New(repo)
	original, err := pool.ReserveMatchingLimit(context.Background(), []string{"basic"}, nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.RotateAccountTokens("old-at", "new-at", "", "", map[string]any{"status": "正常"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	duplicate, err := pool.ReserveMatchingLimit(ctx, []string{"basic"}, nil, nil, 1)
	if duplicate != nil {
		pool.Release(duplicate)
		t.Fatal("rotation bypassed the inflight limit")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected capacity wait: %v", err)
	}
	pool.Feedback(original.Account, 401, errors.New("old token expired"))
	pool.Release(original)
	current, err := repo.AccountList()
	if err != nil {
		t.Fatal(err)
	}
	if current[0]["status"] != "正常" || intValue(current[0]["invalid_count"]) != 0 || intValue(current[0]["fail"]) != 1 {
		t.Fatal("old token failure changed current credential health or lost its count")
	}
	lease, err := pool.ReserveMatchingLimit(context.Background(), []string{"basic"}, nil, nil, 1)
	if err != nil || lease.Account.Token != "new-at" {
		t.Fatal("current credentials unavailable after old lease release")
	}
	pool.Release(lease)
}

func TestExistingGenerationCanBeRetrievedWithoutRemainingQuota(t *testing.T) {
	repo := store.New(filepath.Join(t.TempDir(), "accounts.json"), "", "")
	_, _, _, err := repo.AddAccounts(nil, []map[string]any{{"id": "account", "access_token": "at", "status": "限流", "quota": 0}})
	if err != nil {
		t.Fatal(err)
	}
	pool := New(repo)
	lease, err := pool.ReserveExisting(context.Background(), func(account Account) bool { return store.AccountID(account.Fields) == "account" }, 1)
	if err != nil {
		t.Fatal(err)
	}
	pool.Release(lease)
	if _, _, err := repo.UpdateAccount("at", map[string]any{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ReserveExisting(context.Background(), nil, 1); !errors.Is(err, ErrUnavailable) {
		t.Fatal("disabled account was used for retrieval")
	}
}
