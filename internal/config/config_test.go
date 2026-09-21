package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDataDirectoryOwnsAllDefaultPersistentPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GPT2API_DATA_DIR", "persist")
	for _, key := range []string{"GO_CONFIG_PATH", "GO_ACCOUNTS_PATH", "GO_AUTH_KEYS_PATH", "GO_IMAGE_DATA_DIR", "GO_VIDEO_DATA_DIR", "GO_REGISTER_PATH"} {
		t.Setenv(key, "")
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "persist")
	for _, path := range []string{cfg.ConfigPath, cfg.AccountsPath, cfg.AuthKeysPath, cfg.ImageDataDir, cfg.VideoDataDir, cfg.RegisterPath} {
		rel, err := filepath.Rel(base, path)
		if err != nil || rel == ".." || filepath.IsAbs(rel) {
			t.Fatalf("path outside configured data directory: %s", path)
		}
		if len(rel) > 3 && rel[:3] == "../" {
			t.Fatalf("path outside configured data directory: %s", path)
		}
	}
}

func TestAuthEnvironmentAliasesPreserveExistingInstallations(t *testing.T) {
	t.Setenv("GPT2API_AUTH_KEY", "")
	t.Setenv("CHATGPT2API_AUTH_KEY", "legacy-test-key")
	cfg, err := Load(t.TempDir())
	if err != nil || cfg.APIKey != "legacy-test-key" {
		t.Fatal("legacy key not retained")
	}
	t.Setenv("GPT2API_AUTH_KEY", "new-test-key")
	cfg, err = Load(t.TempDir())
	if err != nil || cfg.APIKey != "new-test-key" {
		t.Fatal("new key did not take precedence")
	}
}

func TestLegacyRootConfigCopiedWithoutOverwritingDataConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GO_CONFIG_PATH", "")
	t.Setenv("GPT2API_DATA_DIR", "data")
	original := []byte(`{"admin_key":"original-test-key","unknown_compat_field":true}`)
	if err := os.WriteFile(filepath.Join(root, "config.json"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(cfg.ConfigPath)
	if err != nil || string(copied) != string(original) {
		t.Fatal("legacy config not preserved")
	}
	saved := []byte(`{"admin_key":"saved-test-key"}`)
	if err := os.WriteFile(cfg.ConfigPath, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err != nil {
		t.Fatal(err)
	}
	copied, _ = os.ReadFile(cfg.ConfigPath)
	if string(copied) != string(saved) {
		t.Fatal("existing data config overwritten")
	}
}
