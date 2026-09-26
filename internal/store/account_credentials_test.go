package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCredentialCASPreservesConcurrentCountersAndRejectsTokenCollision(t *testing.T) {
	root := t.TempDir()
	repo := New(filepath.Join(root, "accounts.json"), "", "")
	_, _, items, err := repo.AddAccounts(nil, []map[string]any{{"id": "a", "access_token": "at-a", "refresh_token": "rt-a"}, {"id": "b", "access_token": "at-b"}})
	if err != nil {
		t.Fatal(err)
	}
	version := CredentialsOf(items[0])
	if _, err := repo.RecordAccountResult("at-a", "a", true); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveAccountCredentials(version, "at-b", "rt-new", ""); !errors.Is(err, ErrCredentialsChanged) {
		t.Fatalf("collision: %v", err)
	}
	updated, err := repo.SaveAccountCredentials(version, "at-new", "rt-new", "id-new")
	if err != nil {
		t.Fatal(err)
	}
	if updated["success"] != float64(1) {
		t.Fatalf("counter lost: %v", updated["success"])
	}
	if _, err := repo.UpdateAccountIfCredentialsMatch(version, map[string]any{"quota": 0}); !errors.Is(err, ErrCredentialsChanged) {
		t.Fatalf("old version accepted: %v", err)
	}
	saved, err := New(filepath.Join(root, "accounts.json"), "", "").AccountList()
	if err != nil || saved[0]["refresh_token"] != "rt-new" {
		t.Fatal("credentials not durable")
	}
}
