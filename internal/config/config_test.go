package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestEditableModelAndReasoningEffortDefaults(t *testing.T) {
	t.Setenv("GO_EDITABLE_MODEL", "")
	t.Setenv("GO_EDITABLE_REASONING_EFFORT", "")
	cfg, err := Load(t.TempDir())
	if err != nil || cfg.EditableModel != "gpt-5-5-thinking" || cfg.EditableReasoningEffort != "extended" {
		t.Fatalf("defaults: %q %q %v", cfg.EditableModel, cfg.EditableReasoningEffort, err)
	}
	t.Setenv("GO_EDITABLE_MODEL", "gpt-5-5")
	t.Setenv("GO_EDITABLE_REASONING_EFFORT", "low")
	cfg, err = Load(t.TempDir())
	if err != nil || cfg.EditableModel != "gpt-5-5" || cfg.EditableReasoningEffort != "low" {
		t.Fatalf("override: %q %q %v", cfg.EditableModel, cfg.EditableReasoningEffort, err)
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

func TestRuntimeFieldsPreferConfigJSONWhenEnvUnset(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{
		"GPT2API_AUTH_KEY", "CHATGPT2API_AUTH_KEY", "GPT2API_ADMIN_KEY", "CHATGPT2API_ADMIN_KEY",
		"GPT2API_WEBUI_KEY", "CHATGPT2API_WEBUI_KEY", "GO_PUBLIC_BASE_URL", "CHATGPT2API_BASE_URL",
		"GO_REQUEST_TIMEOUT_SECONDS", "GO_CHAT_MAX_RETRIES", "GO_IMAGE_ACCOUNT_CONCURRENCY",
		"GO_IMAGE_MAX_CONCURRENCY", "GO_IMAGE_RETENTION_DAYS", "GO_IMAGE_TASK_WORKERS",
		"GO_IMAGE_TASK_QUEUE_SIZE", "GO_IMAGE_TASK_RETENTION_HOURS", "GO_EDITABLE_MODEL",
		"GO_EDITABLE_REASONING_EFFORT", "GO_CLEARANCE_ENABLED", "GO_FLARESOLVERR_URL",
		"GO_UPSTREAM_URL", "GO_UPSTREAM_API_KEY", "GO_CONFIG_PATH", "GPT2API_DATA_DIR",
	} {
		t.Setenv(key, "")
	}
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{
		"auth-key": "json-api-key",
		"admin_key": "json-admin-key",
		"webui_key": "json-webui-key",
		"public_base_url": "https://files.example.test",
		"request_timeout_seconds": 90,
		"chat_max_retries": 1,
		"image_account_concurrency": 3,
		"image_max_concurrency": 16,
		"image_retention_days": 7,
		"image_task_workers": 4,
		"image_task_queue_size": 8,
		"image_task_retention_hours": 12,
		"editable_model": "gpt-5-5",
		"editable_reasoning_effort": "low",
		"flaresolverr_url": "http://flaresolverr.example.test:8191",
		"clearance_enabled": true,
		"upstream_url": "http://upstream.example.test",
		"GPT2API_PORT": "9999",
		"GPT2API_IMAGE": "should-not-apply"
	}`)
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "json-api-key" || cfg.AdminKey != "json-admin-key" || cfg.WebUIKey != "json-webui-key" {
		t.Fatalf("keys: %+v", cfg)
	}
	if cfg.PublicBaseURL != "https://files.example.test" || os.Getenv("GO_PUBLIC_BASE_URL") != "https://files.example.test" {
		t.Fatalf("public base: %q env=%q", cfg.PublicBaseURL, os.Getenv("GO_PUBLIC_BASE_URL"))
	}
	if cfg.RequestTimeout != 90*time.Second || cfg.ChatMaxRetries != 1 || cfg.ImageAccountLimit != 3 || cfg.ImageMaxConcurrency != 16 {
		t.Fatalf("timeouts/concurrency: %+v", cfg)
	}
	if cfg.ImageRetentionDays != 7 || cfg.ImageTaskWorkers != 4 || cfg.ImageTaskQueueSize != 8 || cfg.ImageTaskRetention != 12*time.Hour {
		t.Fatalf("image tasks: %+v", cfg)
	}
	if cfg.EditableModel != "gpt-5-5" || cfg.EditableReasoningEffort != "low" {
		t.Fatalf("editable: %q %q", cfg.EditableModel, cfg.EditableReasoningEffort)
	}
	if !cfg.ClearanceEnabled || cfg.FlareSolverrURL != "http://flaresolverr.example.test:8191" || cfg.UpstreamURL != "http://upstream.example.test" {
		t.Fatalf("clearance/upstream: %+v", cfg)
	}
	if cfg.ListenAddr != ":8080" || cfg.Version != "1.3.0" {
		t.Fatalf("compose-only JSON keys leaked: listen=%q version=%q", cfg.ListenAddr, cfg.Version)
	}
}

func TestRuntimeEnvOverridesConfigJSON(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GO_CONFIG_PATH", "")
	t.Setenv("GPT2API_DATA_DIR", "data")
	t.Setenv("GO_EDITABLE_MODEL", "env-model")
	t.Setenv("GO_REQUEST_TIMEOUT_SECONDS", "120")
	t.Setenv("GO_PUBLIC_BASE_URL", "https://env.example.test")
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"editable_model":"json-model","request_timeout_seconds":90,"public_base_url":"https://json.example.test"}`)
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EditableModel != "env-model" || cfg.RequestTimeout != 120*time.Second || cfg.PublicBaseURL != "https://env.example.test" {
		t.Fatalf("env did not override: model=%q timeout=%v base=%q", cfg.EditableModel, cfg.RequestTimeout, cfg.PublicBaseURL)
	}
}
