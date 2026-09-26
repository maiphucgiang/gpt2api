package bps

import (
	"time"

	"github.com/maiphucgiang/gpt2api/internal/store"
)

func conversationIdentity(source Object) string {
	if key := String(source["prompt_cache_key"]); key != "" {
		return key
	}
	if key := String(object(source["client_metadata"])["session_id"]); key != "" {
		return key
	}
	values := list(source["input"])
	if text, ok := source["input"].(string); ok {
		values = []any{message("user", text)}
	}
	root := []any{}
	for _, raw := range values {
		item := object(raw)
		role := String(item["role"])
		if role == "" {
			continue
		}
		content := item["content"]
		if text, ok := content.(string); ok {
			content = message(role, text)["content"]
		}
		root = append(root, Object{"type": "message", "role": role, "content": content})
		if role == "user" {
			break
		}
	}
	return hash(String(source["instructions"]), string(encode(root)))
}

// AccountForRequest also pins encrypted reasoning that contains no tool calls.
func (r *Replay) AccountForRequest(owner, model string, source Object) (string, error) {
	account, err := r.AccountFor(owner, model, source["input"])
	if err != nil {
		return "", err
	}
	entry, ok := r.get(owner, model, "@session:"+conversationIdentity(source))
	if ok {
		if account != "" && account != entry.Account {
			return "", fail(409, "mixed_account_history", "conversation and tool history use different accounts")
		}
		account = entry.Account
	}
	if account == "" {
		for _, raw := range list(source["input"]) {
			item := object(raw)
			if String(item["encrypted_content"]) != "" {
				return "", fail(409, "reasoning_history_missing", "encrypted history requires its original retained BPS session")
			}
		}
	}
	return account, nil
}

func (r *Replay) Bind(owner, model, account string, source Object) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return fail(503, "replay_storage_unavailable", "BPS session storage is unavailable")
	}
	key := replayKey(owner, model, "@session:"+conversationIdentity(source))
	if entry, ok := r.entries[key]; ok && entry.Expires.After(time.Now()) {
		if entry.Account != account {
			return fail(409, "concurrent_session_binding", "conversation was concurrently assigned to another account; retry with the same history")
		}
		return nil
	}
	next := map[string]replayEntry{}
	now := time.Now()
	for k, entry := range r.entries {
		if entry.Expires.After(now) {
			next[k] = entry
		}
	}
	next[key] = replayEntry{Account: account, Expires: now.Add(24 * time.Hour)}
	for len(next) > replayLimit || replaySize(next) > replayBytes {
		oldest := ""
		var earliest time.Time
		for k, entry := range next {
			if k != key && (oldest == "" || entry.Expires.Before(earliest)) {
				oldest = k
				earliest = entry.Expires
			}
		}
		if oldest == "" {
			return fail(503, "session_storage_full", "BPS session storage limit exceeded")
		}
		delete(next, oldest)
	}
	if r.path != "" {
		if err := store.WriteJSONFile(r.path, next); err != nil {
			return fail(503, "replay_write_failed", "cannot persist BPS conversation identity")
		}
	}
	r.entries = next
	return nil
}
