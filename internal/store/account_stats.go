package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// AccountID keeps existing public references stable across credential rotation.
func AccountID(item map[string]any) string {
	for _, key := range []string{"id", "account_ref", "account_id", "chatgpt_account_id", "user_id", "email"} {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			if key == "email" {
				return strings.ToLower(strings.TrimSpace(value))
			}
			return strings.TrimSpace(value)
		}
	}
	token := accountToken(item)
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return "acct_" + hex.EncodeToString(sum[:8])
}

// RecordAccountResult increments a business attempt under the account store lock.
func (s *Store) RecordAccountResult(token, id string, success bool) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadAccountsLocked(); err != nil {
		return nil, err
	}
	index, found := s.accountsIndex[token]
	if !found && id != "" {
		for i, item := range s.accountsCache {
			if AccountID(item) == id {
				index, found = i, true
				break
			}
		}
	}
	if !found {
		return nil, os.ErrNotExist
	}
	next := cloneMap(s.accountsCache[index])
	key := "fail"
	if success {
		key = "success"
	}
	count, _ := strconv.ParseFloat(fmt.Sprint(next[key]), 64)
	if count < 0 {
		count = 0
	}
	next[key] = count + 1
	next["last_used_at"] = time.Now().UTC().Format(time.RFC3339)
	items := append([]map[string]any(nil), s.accountsCache...)
	items[index] = next
	s.replaceAccountsLocked(items)
	s.accountsDirty = true
	s.scheduleAccountFlushLocked(accountRuntimeFlushDelay)
	return cloneMap(next), nil
}
