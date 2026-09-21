package register

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Store persists GPT registration and account-maintenance settings.
type Store struct {
	configPath string
	mu         sync.Mutex
}

func New(configPath string) *Store { return &Store{configPath: configPath} }

func (s *Store) Get() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked()
}

func (s *Store) Update(updates map[string]any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.getLocked()
	for key, value := range updates {
		if key != "logs" && key != "target" {
			current[key] = value
		}
	}
	if err := writeJSON0600(s.configPath, current); err != nil {
		return nil, err
	}
	return cloneMap(current), nil
}

func (s *Store) Start() (map[string]any, error) { return s.SetEnabled(true) }
func (s *Store) Stop() (map[string]any, error)  { return s.SetEnabled(false) }

func (s *Store) SetEnabled(enabled bool) (map[string]any, error) {
	return s.Update(map[string]any{"enabled": enabled})
}

func (s *Store) Reset() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := defaultConfig()
	if err := writeJSON0600(s.configPath, value); err != nil {
		return nil, err
	}
	return cloneMap(value), nil
}

func (s *Store) StopCheckoutRetries() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.getLocked()
	items := checkoutTasks(value)
	for _, item := range items {
		switch strings.ToLower(stringValue(item["status"])) {
		case "queued", "running", "retrying", "pending":
			item["status"] = "cancelled"
			item["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		}
	}
	value["checkout_tasks"] = items
	value["checkout_retries_active"] = false
	value["checkout_retry_job_count"] = 0
	if err := writeJSON0600(s.configPath, value); err != nil {
		return nil, err
	}
	return cloneMap(value), nil
}

func (s *Store) ClearCheckoutHistory() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.getLocked()
	items := checkoutTasks(value)
	active := map[string]bool{"queued": true, "running": true, "retrying": true, "pending": true}
	kept := make([]map[string]any, 0, len(items))
	removed := 0
	for _, item := range items {
		if active[strings.ToLower(stringValue(item["status"]))] {
			kept = append(kept, item)
		} else {
			removed++
		}
	}
	value["checkout_tasks"] = kept
	if err := writeJSON0600(s.configPath, value); err != nil {
		return nil, err
	}
	return map[string]any{"removed": removed, "register": cloneMap(value)}, nil
}

func (s *Store) ResetOutlookPoolState(scope string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.getLocked()
	state := map[string]any{}
	if existing, ok := value["outlook_pool_state"].(map[string]any); ok {
		state = cloneMap(existing)
	}
	state["last_reset_scope"] = strings.TrimSpace(scope)
	state["last_reset_at"] = time.Now().UTC().Format(time.RFC3339)
	state["reset_count"] = intValue(state["reset_count"]) + 1
	state["retryable"], state["invalid"] = []any{}, []any{}
	if strings.EqualFold(strings.TrimSpace(scope), "unused") {
		state["unused_pruned_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	value["outlook_pool_state"] = state
	if err := writeJSON0600(s.configPath, value); err != nil {
		return nil, err
	}
	return cloneMap(value), nil
}

func (s *Store) QueueOutlookRetry(providerID string, mailboxIDs []string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.getLocked()
	state := map[string]any{}
	if existing, ok := value["outlook_pool_state"].(map[string]any); ok {
		state = cloneMap(existing)
	}
	state["last_retry_provider_id"] = strings.TrimSpace(providerID)
	state["last_retry_at"] = time.Now().UTC().Format(time.RFC3339)
	state["retryable"] = stringAnyList(mailboxIDs)
	state["retry_executor_available"] = false
	state["retry_executor_error"] = "Outlook token/Graph registration worker is not configured in Go runtime"
	value["outlook_pool_state"] = state
	if err := writeJSON0600(s.configPath, value); err != nil {
		return nil, err
	}
	return cloneMap(value), nil
}

func (s *Store) getLocked() map[string]any {
	value := defaultConfig()
	if raw, err := os.ReadFile(s.configPath); err == nil {
		var loaded map[string]any
		if json.Unmarshal(raw, &loaded) == nil {
			for key, item := range loaded {
				value[key] = item
			}
		}
	}
	value["target"] = "openai"
	return value
}

func defaultConfig() map[string]any {
	return map[string]any{
		"enabled": false, "target": "openai", "total": 0, "threads": 2,
		"mode": "register", "target_quota": 0, "target_available": 0, "check_interval": 60,
		"stats": map[string]any{"done": 0, "success": 0, "fail": 0, "running": 0},
		"logs":  []any{}, "checkout_tasks": []any{}, "checkout_retries_active": false,
		"checkout_retry_job_count": 0,
		"openai_survival":          map[string]any{"enabled": true, "interval_minutes": 60, "concurrency": 4, "refresh_codex_rt": true},
	}
}

func checkoutTasks(value map[string]any) []map[string]any {
	raw, _ := value["checkout_tasks"].([]any)
	result := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if object, ok := item.(map[string]any); ok {
			result = append(result, cloneMap(object))
		}
	}
	return result
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		var result int
		_, _ = fmt.Sscanf(strings.TrimSpace(typed), "%d", &result)
		return result
	default:
		return 0
	}
}

func stringAnyList(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		if clean := strings.TrimSpace(value); clean != "" {
			result = append(result, clean)
		}
	}
	return result
}

func cloneMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func writeJSON0600(path string, value any) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("empty storage path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".register-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func NewID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("register-%d", time.Now().UnixNano())
	}
	return "register-" + hex.EncodeToString(raw)
}
