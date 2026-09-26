package store

import (
	"errors"
	"strings"
	"time"
)

var ErrCredentialsChanged = errors.New("account credentials changed during refresh; retry with current credentials")

// CredentialVersion identifies the credential set, independently of usage counters.
type CredentialVersion struct {
	id, accessToken, refreshToken, idToken, refreshedAt string
}

func CredentialsOf(account map[string]any) CredentialVersion {
	return CredentialVersion{AccountID(account), accountToken(account), credentialField(account, "refresh_token"), credentialField(account, "id_token"), credentialField(account, "last_token_refresh_at")}
}

func credentialField(account map[string]any, key string) string {
	value, _ := account[key].(string)
	return strings.TrimSpace(value)
}

// UpdateAccountIfCredentialsMatch rejects results produced for superseded credentials.
func (s *Store) UpdateAccountIfCredentialsMatch(expected CredentialVersion, updates map[string]any) (map[string]any, error) {
	return s.updateAccountIfCredentialsMatch(expected, updates, false)
}

func (s *Store) updateAccountIfCredentialsMatch(expected CredentialVersion, updates map[string]any, retainCredentials bool) (map[string]any, error) {
	s.accountsWriteMu.Lock()
	defer s.accountsWriteMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadAccountsLocked(); err != nil {
		return nil, err
	}
	index, ok := s.accountsIndex[expected.accessToken]
	if !ok || CredentialsOf(s.accountsCache[index]) != expected {
		return nil, ErrCredentialsChanged
	}
	if token := credentialField(updates, "access_token"); token != "" {
		if other, exists := s.accountsIndex[token]; exists && other != index {
			return nil, ErrCredentialsChanged
		}
	}
	items, next, ok := s.updatedAccountsLocked(expected.accessToken, updates)
	if !ok {
		return nil, ErrCredentialsChanged
	}
	if err := writeJSON(s.accountsPath, items); err != nil {
		if retainCredentials {
			s.replaceAccountsLocked(items)
			s.accountsDirty = true
			s.scheduleAccountFlushLocked(accountRuntimeFlushDelay)
			return cloneMap(next), err
		}
		return nil, err
	}
	s.replaceAccountsLocked(items)
	s.accountsDirty = false
	return cloneMap(next), nil
}

func (s *Store) SaveAccountCredentials(expected CredentialVersion, access, refresh, id string) (map[string]any, error) {
	updates := map[string]any{"last_token_refresh_at": time.Now().UTC().Format(time.RFC3339Nano), "last_token_refresh_error": nil, "last_token_refresh_error_at": nil}
	for key, value := range map[string]string{"access_token": access, "refresh_token": refresh, "id_token": id} {
		if value = strings.TrimSpace(value); value != "" {
			updates[key] = value
		}
	}
	return s.updateAccountIfCredentialsMatch(expected, updates, true)
}

func (s *Store) CompleteAccountRefresh(expected CredentialVersion, fields map[string]any) (map[string]any, error) {
	return s.UpdateAccountIfCredentialsMatch(expected, healthyAccountFields(fields))
}

func healthyAccountFields(fields map[string]any) map[string]any {
	updates := cloneMap(fields)
	updates["invalid_count"] = 0
	for _, key := range []string{"cooldown_until", "next_retry_at", "last_refresh_error", "last_refresh_error_at", "last_refresh_warning", "last_refresh_warning_at", "last_error_kind", "last_error_status", "last_error_message", "last_error_at", "status_reason_code"} {
		updates[key] = nil
	}
	return updates
}
