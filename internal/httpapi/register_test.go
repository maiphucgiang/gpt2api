package httpapi

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestRegisterCheckoutControlsAndRefreshSettings(t *testing.T) {
	cfg := adminTestConfig(t.TempDir())
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"checkout_tasks":[{"id":"done","status":"completed"},{"id":"waiting","status":"queued"},{"id":"retry","status":"retrying"}]}`
	if err := os.WriteFile(cfg.RegisterPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	server := New(cfg)
	t.Cleanup(func() { _ = server.Close() })
	handler := server.Handler()
	stopped := adminRequest(handler, http.MethodPost, "/api/register/checkout-retries/stop", nil)
	if stopped.Code != http.StatusOK || !strings.Contains(stopped.Body.String(), `"checkout_retries_active":false`) {
		t.Fatalf("stop: %d", stopped.Code)
	}
	cleared := adminRequest(handler, http.MethodPost, "/api/register/checkout-history/clear", nil)
	if cleared.Code != http.StatusOK || !strings.Contains(cleared.Body.String(), `"removed":3`) {
		t.Fatalf("clear: %d", cleared.Code)
	}
	updated := adminRequest(handler, http.MethodPost, "/api/register/openai/survival", strings.NewReader(`{"enabled":true,"interval_minutes":30,"concurrency":2}`))
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d", updated.Code)
	}
	value := adminRequest(handler, http.MethodGet, "/api/register/openai/survival", nil)
	if value.Code != http.StatusOK || !strings.Contains(value.Body.String(), `"interval_minutes":30`) {
		t.Fatal("refresh settings not persisted")
	}
}
