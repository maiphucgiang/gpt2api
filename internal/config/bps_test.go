package config

import (
	"testing"
	"time"
)

func TestBPSConfigurationIsOptInAndBounded(t *testing.T) {
	t.Setenv("GO_BPS_ENABLED", "")
	t.Setenv("GO_BPS_BASE_URL", "")
	t.Setenv("GO_BPS_TIMEOUT_SECONDS", "")
	root := t.TempDir()
	cfg, err := Load(root)
	if err != nil || cfg.BPSEnabled || cfg.BPSBaseURL != "https://bps.openai.com" || cfg.BPSTimeout != 180*time.Second {
		t.Fatalf("unexpected default BPS config: %v", err)
	}
	t.Setenv("GO_BPS_ENABLED", "true")
	t.Setenv("GO_BPS_BASE_URL", "http://127.0.0.1:12345/")
	t.Setenv("GO_BPS_TIMEOUT_SECONDS", "9999")
	cfg, err = Load(root)
	if err != nil || !cfg.BPSEnabled || cfg.BPSBaseURL != "http://127.0.0.1:12345" || cfg.BPSTimeout != 900*time.Second {
		t.Fatal("BPS environment override failed", err)
	}
}
