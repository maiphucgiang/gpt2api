package store

import (
	"errors"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accountpolicy"
)

var ErrExternalIdentity = errors.New("external credential identity or ownership conflicts with the saved account")

func ExternalIdentityMatches(old, next map[string]any) bool {
	oldSubject, oldWorkspace := accountpolicy.Identity(old)
	subject, workspace := accountpolicy.Identity(next)
	if oldSubject == "" {
		oldSubject = credentialField(old, "credential_subject")
	}
	if oldWorkspace == "" {
		oldWorkspace = credentialField(old, "credential_workspace_id")
	}
	return (oldSubject == "" || subject == oldSubject) && (oldWorkspace == "" || workspace == oldWorkspace)
}

func externalUpdates(payload map[string]any) map[string]any {
	updates := map[string]any{
		"refresh_token": nil, "last_credential_sync_at": time.Now().UTC().Format(time.RFC3339),
		"credential_sync_status": "ready", "credential_sync_error": nil,
		"credential_sync_retry_at": nil, "credential_sync_failures": 0,
	}
	for _, key := range []string{"access_token", "id_token", "account_id", "chatgpt_account_id", "email", "expires_at", "expired", "credential_owner", "credential_source_id", "credential_remote_id", "credential_subject", "credential_workspace_id"} {
		if value, exists := payload[key]; exists {
			updates[key] = value
		}
	}
	return updates
}

func (s *Store) AccountCredentialVersions() (map[string]CredentialVersion, error) {
	items, err := s.AccountList()
	if err != nil {
		return nil, err
	}
	versions := make(map[string]CredentialVersion, len(items))
	for _, item := range items {
		versions[AccountID(item)] = CredentialsOf(item)
	}
	return versions, nil
}

// UpsertExternalAccount updates a source binding without replacing local identity or preferences.
func (s *Store) UpsertExternalAccount(payload map[string]any, versions map[string]CredentialVersion) (added, updated int, err error) {
	owner := accountpolicy.Owner(payload)
	source, remote := credentialField(payload, "credential_source_id"), credentialField(payload, "credential_remote_id")
	if (owner != "cpa" && owner != "sub2api") || source == "" || remote == "" || accountToken(payload) == "" {
		return 0, 0, ErrExternalIdentity
	}
	s.accountsWriteMu.Lock()
	defer s.accountsWriteMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadAccountsLocked(); err != nil {
		return 0, 0, err
	}
	items := cloneAccountList(s.accountsCache)
	index := -1
	for i, item := range items {
		bound := accountpolicy.Owner(item) == owner && credentialField(item, "credential_source_id") == source && credentialField(item, "credential_remote_id") == remote
		if bound || accountToken(item) == accountToken(payload) {
			if index != -1 {
				return 0, 0, ErrExternalIdentity
			}
			if versions[AccountID(item)] != CredentialsOf(item) {
				return 0, 0, ErrCredentialsChanged
			}
			if !bound && credentialField(item, "credential_owner") != "" && accountpolicy.Owner(item) != "external_unbound" {
				return 0, 0, ErrExternalIdentity
			}
			if !ExternalIdentityMatches(item, payload) {
				return 0, 0, ErrExternalIdentity
			}
			index = i
		}
	}
	if index < 0 {
		next := cloneMap(payload)
		for key, value := range externalUpdates(payload) {
			next[key] = value
		}
		normalizeAccount(next)
		items = append(items, next)
		added = 1
	} else {
		next := cloneMap(items[index])
		for key, value := range externalUpdates(payload) {
			next[key] = value
		}
		if accountToken(next) != accountToken(items[index]) {
			next["subscription_checked_at"] = nil
			next["last_quota_refresh_at"] = nil
		}
		items[index] = next
		updated = 1
	}
	items = normalizeAccountListJSON(items)
	if err := writeJSON(s.accountsPath, items); err != nil {
		return 0, 0, err
	}
	s.replaceAccountsLocked(items)
	s.accountsDirty = false
	return added, updated, nil
}

func (s *Store) SaveExternalCredentials(expected CredentialVersion, old, payload map[string]any) (map[string]any, error) {
	if !ExternalIdentityMatches(old, payload) || credentialField(payload, "credential_owner") != credentialField(old, "credential_owner") || credentialField(payload, "credential_source_id") != credentialField(old, "credential_source_id") || credentialField(payload, "credential_remote_id") != credentialField(old, "credential_remote_id") {
		return nil, ErrExternalIdentity
	}
	updates := externalUpdates(payload)
	if accountToken(payload) != accountToken(old) {
		updates["subscription_checked_at"] = nil
	}
	return s.updateAccountIfCredentialsMatch(expected, updates, true)
}
