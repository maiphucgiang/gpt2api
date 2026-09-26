package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
)

func TestAccountBPSOptOutKeepsWebAvailability(t *testing.T) {
	s := bpsTestServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("disabled BPS account reached upstream") })
	update := httptest.NewRequest(http.MethodPost, "/api/accounts/update", strings.NewReader(`{"access_token":"account","bps_enabled":false,"status":"正常","quota":10}`))
	update.Header.Set("Authorization", "Bearer "+s.cfg.AdminKey)
	changed := httptest.NewRecorder()
	s.updateAccount(changed, update)
	if changed.Code != 200 {
		t.Fatal(changed.Code, changed.Body.String())
	}
	rows, _ := s.store.AccountList()
	a := accounts.Account{Token: accountToken(rows[0]), Fields: rows[0]}
	if s.bpsAvailable(a, "bsg/gpt-6-astra", "") {
		t.Fatal("BPS-only opt-out ignored")
	}
	lease, err := s.accountPool.Reserve(context.Background(), []string{"basic"}, nil)
	if err != nil {
		t.Fatal("BPS opt-out disabled Web account", err)
	}
	s.accountPool.Release(lease)
	response := bpsRequest(s, "/v1/responses", s.cfg.APIKey, map[string]any{"model": "bsg/gpt-6-astra", "input": "hello"})
	if response.Code != 503 {
		t.Fatal(response.Code, response.Body.String())
	}
	bad := httptest.NewRequest(http.MethodPost, "/api/accounts/update", strings.NewReader(`{"access_token":"account","bps_enabled":"false"}`))
	bad.Header.Set("Authorization", "Bearer "+s.cfg.AdminKey)
	rejected := httptest.NewRecorder()
	s.updateAccount(rejected, bad)
	if rejected.Code != 400 {
		t.Fatal("nonboolean BPS opt-out accepted")
	}
}
