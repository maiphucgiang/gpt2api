package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
	"github.com/maiphucgiang/gpt2api/internal/store"
)

func TestExternalUnchangedSourceBacksOffButKeepsUsableAT(t *testing.T) {
	f := newExternalCredentialFixture(t, "cpa", time.Now().Add(3*time.Minute))
	f.next = f.old
	for i := 0; i < 2; i++ {
		rows, _ := f.s.store.AccountList()
		a := accounts.Account{Token: f.old, Fields: rows[0]}
		called := false
		err := f.s.runAccountBusiness(context.Background(), &a, func() error { called = true; return nil }, func(error) bool { return true })
		if err != nil || !called || a.Token != f.old {
			t.Fatal("valid AT was retired while waiting for source", err)
		}
	}
	rows, _ := f.s.store.AccountList()
	if rows[0]["credential_sync_status"] != "pending" || f.reads.Load() != 1 || f.oauth.Load() != 0 {
		t.Fatal("unchanged source not backed off")
	}
}

func TestSourceBindingAPIKeepsIdentityAndRemovesRT(t *testing.T) {
	f := newExternalCredentialFixture(t, "cpa", time.Now().Add(time.Hour))
	f.next = f.old
	rows, _ := f.s.store.AccountList()
	_, err := f.s.store.UpdateAccountIfCredentialsMatch(store.CredentialsOf(rows[0]), map[string]any{"credential_owner": "external_unbound", "credential_source_id": nil, "credential_remote_id": nil})
	if err != nil {
		t.Fatal(err)
	}
	w := adminRequest(f.s.Handler(), http.MethodPost, "/api/accounts/source", strings.NewReader(`{"account_ref":"external","credential_owner":"cpa","credential_source_id":"source","credential_remote_id":"42"}`))
	if w.Code != 200 || strings.Contains(w.Body.String(), f.old) || strings.Contains(w.Body.String(), "legacy-rt") {
		t.Fatal("binding failed or exposed credentials", w.Code)
	}
	rows, _ = f.s.store.AccountList()
	if len(rows) != 1 || rows[0]["id"] != "external" || rows[0]["credential_owner"] != "cpa" || rows[0]["refresh_token"] != nil || intValue(rows[0]["success"]) != 8 {
		t.Fatal("binding damaged account")
	}
}

func TestExternalRemoteImportsUpdateInsteadOfDuplicating(t *testing.T) {
	for _, owner := range []string{"cpa", "sub2api"} {
		t.Run(owner, func(t *testing.T) {
			f := newExternalCredentialFixture(t, owner, time.Now().Add(time.Hour))
			job := jobFor(1)
			if owner == "cpa" {
				source, _ := f.s.external.getCPA("source")
				f.s.runCPAImport(source, []string{"42"}, job)
			} else {
				source, _ := f.s.external.getSub("source")
				f.s.runSubImport(source, []string{"42"}, job)
			}
			rows, _ := f.s.store.AccountList()
			if len(rows) != 1 || rows[0]["access_token"] != f.next || rows[0]["refresh_token"] != nil || job["status"] != "completed" || intValue(job["refreshed"]) != 1 || intValue(job["added"]) != 0 {
				t.Fatal("remote import did not update the source binding")
			}
		})
	}
}

func TestDeletedConnectionCannotCompleteInFlightSync(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "codex", "access_token": "new"})
	}))
	defer u.Close()
	s := New(adminTestConfig(t.TempDir()))
	defer s.Close()
	source := externalCPAPool{ID: "source", BaseURL: u.URL}
	if err := s.external.saveCPAList([]externalCPAPool{source}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := s.readCPACredential(context.Background(), source, "selected"); result <- err }()
	<-started
	if err := s.external.saveCPAList(nil); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err == nil {
		t.Fatal("deleted connection still authorized a credential update")
	}
}
