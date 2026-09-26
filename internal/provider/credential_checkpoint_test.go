package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCredentialCheckpointFailureStopsProfileProbe(t *testing.T) {
	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "new-at", "refresh_token": "new-rt"})
			return
		}
		probes.Add(1)
		http.Error(w, "unexpected profile", 500)
	}))
	defer upstream.Close()
	client := NewOpenAIAccountClient(upstream.URL, upstream.URL+"/oauth/token", upstream.Client(), nil)
	saveErr := errors.New("storage unavailable")
	result, err := client.RefreshWithCheckpoint(context.Background(), map[string]any{"access_token": "old-at", "refresh_token": "old-rt"}, true, func(AccountRefreshResult) error { return saveErr })
	if !errors.Is(err, saveErr) || probes.Load() != 0 {
		t.Fatal("profile request issued without a successful checkpoint")
	}
	if result.AccessToken != "new-at" || result.RefreshToken != "new-rt" {
		t.Fatal("caller lost issued credentials on storage error")
	}
}
