package register

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistrationSettingsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "register.json")
	store := New(path)
	value, err := store.Start()
	if err != nil || value["enabled"] != true || value["target"] != "openai" {
		t.Fatalf("start: %v", err)
	}
	_, err = store.Update(map[string]any{"openai_survival": map[string]any{"enabled": true, "interval_minutes": 30}})
	if err != nil {
		t.Fatal(err)
	}
	loaded := New(path).Get()
	if intValue(loaded["openai_survival"].(map[string]any)["interval_minutes"]) != 30 {
		t.Fatal("refresh settings lost")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("settings file must be private")
	}
	if value, err = store.Stop(); err != nil || value["enabled"] != false {
		t.Fatal("stop failed")
	}
}

func TestRuntimeRequiresAllExternalProviders(t *testing.T) {
	runtime := NewRuntime()
	if runtime.Ready("openai") {
		t.Fatal("empty runtime should not be ready")
	}
	if err := runtime.Start("openai"); err != ErrExecutorNotConfigured {
		t.Fatalf("unexpected error: %v", err)
	}
	if runtime.Status("openai")["ready"] != false {
		t.Fatal("unconfigured runtime advertised ready")
	}
}
