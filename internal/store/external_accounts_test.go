package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func externalStoreFixture(token string) map[string]any {
	return map[string]any{"access_token": token, "refresh_token": "must-not-be-retained", "account_id": "workspace", "source_type": "codex", "credential_owner": "cpa", "credential_source_id": "source", "credential_remote_id": "remote.json"}
}

func TestExternalUpsertPreservesLocalIdentityAndPreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	s := New(path, "", "")
	added, _, err := s.UpsertExternalAccount(externalStoreFixture("first"), nil)
	if err != nil || added != 1 {
		t.Fatal(added, err)
	}
	rows, _ := s.AccountList()
	id := AccountID(rows[0])
	if rows[0]["refresh_token"] != nil {
		t.Fatal("source refresh token retained")
	}
	_, err = s.UpdateAccountIfCredentialsMatch(CredentialsOf(rows[0]), map[string]any{"enabled": false, "proxy": "http://127.0.0.1:1080", "group_id": "group", "success": 8, "fail": 3, "subscription_checked_at": "old"})
	if err != nil {
		t.Fatal(err)
	}
	versions, _ := s.AccountCredentialVersions()
	added, updated, err := s.UpsertExternalAccount(externalStoreFixture("second"), versions)
	if err != nil || added != 0 || updated != 1 {
		t.Fatal(added, updated, err)
	}
	rows, err = New(path, "", "").AccountList()
	if err != nil || len(rows) != 1 {
		t.Fatal("source import duplicated account", err)
	}
	x := rows[0]
	if AccountID(x) != id || x["enabled"] != false || x["proxy"] != "http://127.0.0.1:1080" || x["group_id"] != "group" || x["success"] != float64(8) || x["fail"] != float64(3) || x["access_token"] != "second" || x["refresh_token"] != nil || x["subscription_checked_at"] != nil {
		t.Fatal("identity, preferences, counters or verification state changed incorrectly")
	}
	if _, _, err = s.UpsertExternalAccount(externalStoreFixture("third"), versions); !errors.Is(err, ErrCredentialsChanged) {
		t.Fatal("late source response replaced new credentials", err)
	}
	versions, _ = s.AccountCredentialVersions()
	bad := externalStoreFixture("other")
	bad["account_id"] = "other-workspace"
	if _, _, err = s.UpsertExternalAccount(bad, versions); !errors.Is(err, ErrExternalIdentity) {
		t.Fatal("source changed workspace", err)
	}
}

func TestExternalImportOnlyAdoptsUnboundExactCredential(t *testing.T) {
	for _, owner := range []string{"", "external_unbound", "local", "sub2api"} {
		t.Run(owner, func(t *testing.T) {
			s := New(filepath.Join(t.TempDir(), "accounts.json"), "", "")
			_, _, rows, err := s.AddAccounts(nil, []map[string]any{{"id": "stable", "access_token": "same", "account_id": "workspace", "source_type": "codex", "credential_owner": owner, "refresh_token": "old"}})
			if err != nil {
				t.Fatal(err)
			}
			versions, _ := s.AccountCredentialVersions()
			_, updated, err := s.UpsertExternalAccount(externalStoreFixture("same"), versions)
			if owner == "local" || owner == "sub2api" {
				if !errors.Is(err, ErrExternalIdentity) {
					t.Fatal("silently took over another manager", err)
				}
			} else if err != nil || updated != 1 {
				t.Fatal(updated, err)
			}
			after, _ := s.AccountList()
			if len(after) != 1 || AccountID(after[0]) != AccountID(rows[0]) {
				t.Fatal("migration replaced local identity")
			}
		})
	}
}

func TestExternalBindingsCannotAliasAnotherLocalAccount(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "accounts.json"), "", "")
	if _, _, err := s.UpsertExternalAccount(externalStoreFixture("a"), nil); err != nil {
		t.Fatal(err)
	}
	_, _, rows, err := s.AddAccounts(nil, []map[string]any{{"id": "independent", "access_token": "b", "account_id": "workspace", "source_type": "web"}})
	if err != nil {
		t.Fatal(err)
	}
	var independent map[string]any
	for _, x := range rows {
		if x["access_token"] == "b" {
			independent = x
		}
	}
	if independent == nil {
		t.Fatal("fixture not created")
	}
	bound := cloneMap(independent)
	for _, k := range []string{"credential_owner", "credential_source_id", "credential_remote_id"} {
		bound[k] = externalStoreFixture("b")[k]
	}
	if _, err := s.SaveExternalCredentials(CredentialsOf(independent), bound, externalStoreFixture("b")); !errors.Is(err, ErrExternalIdentity) {
		t.Fatal("two local accounts bound to one remote record", err)
	}
}
