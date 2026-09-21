package accounts

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/maiphucgiang/gpt2api/internal/store"
)

func TestFeedbackCountersAreAtomicAndSurviveRotation(t *testing.T) {
	root := t.TempDir()
	repo := store.New(filepath.Join(root, "accounts.json"), filepath.Join(root, "keys.json"), filepath.Join(root, "config.json"))
	_, _, items, err := repo.AddAccounts(nil, []map[string]any{{"id": "stable-account", "access_token": "old-token", "source_type": "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	p := New(repo)
	account := Account{Token: "old-token", Pool: "basic", Fields: items[0]}
	if _, _, err = repo.RotateAccountTokens("old-token", "new-token", "", "", nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.Feedback(account, 200, nil) }()
	}
	wg.Wait()
	p.Feedback(account, 502, errors.New("upstream failure"))
	p.Feedback(account, 499, context.Canceled)
	if err = repo.FlushAccounts(); err != nil {
		t.Fatal(err)
	}
	disk := store.New(filepath.Join(root, "accounts.json"), "", "")
	items, err = disk.AccountList()
	if err != nil {
		t.Fatal(err)
	}
	if intValue(items[0]["success"]) != 50 || intValue(items[0]["fail"]) != 1 {
		t.Fatalf("counts=%v/%v", items[0]["success"], items[0]["fail"])
	}
	if items[0]["last_used_at"] == nil {
		t.Fatal("last_used_at missing")
	}
}

func TestFeedbackKeepsWorkspaceMembersSeparateAfterRotation(t *testing.T) {
	root := t.TempDir()
	repo := store.New(filepath.Join(root, "accounts.json"), "", "")
	_, _, items, err := repo.AddAccounts(nil, []map[string]any{
		{"access_token": "member-a-token", "account_id": "shared-workspace", "email": "a@example.test"},
		{"access_token": "member-b-token", "account_id": "shared-workspace", "email": "b@example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	account := Account{Token: "member-b-token", Fields: items[1]}
	if _, _, err = repo.RotateAccountTokens(account.Token, "member-b-rotated", "", "", nil); err != nil {
		t.Fatal(err)
	}
	New(repo).Feedback(account, 200, nil)
	if err = repo.FlushAccounts(); err != nil {
		t.Fatal(err)
	}
	items, err = repo.AccountList()
	if err != nil {
		t.Fatal(err)
	}
	if intValue(items[0]["success"]) != 0 || intValue(items[1]["success"]) != 1 {
		t.Fatalf("workspace member counts=%v/%v", items[0]["success"], items[1]["success"])
	}
	disk := store.New(filepath.Join(root, "accounts.json"), "", "")
	saved, err := disk.AccountList()
	if err != nil {
		t.Fatal(err)
	}
	if saved[1]["id"] != account.Fields["id"] || saved[0]["id"] == saved[1]["id"] || saved[1]["account_id"] != "shared-workspace" {
		t.Fatal("account reference changed or workspace identity lost")
	}
}
