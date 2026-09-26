package bps

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/store"
)

const replayLimit = 256
const replayBytes = 16 << 20

type replayEntry struct {
	Account string    `json:"account"`
	Native  Object    `json:"native"`
	Client  Object    `json:"client"`
	Expires time.Time `json:"expires"`
}

// Replay keeps original server item IDs partitioned by caller and model.
type Replay struct {
	mu      sync.Mutex
	path    string
	entries map[string]replayEntry
	err     error
}

func NewReplay(path string) *Replay {
	r := &Replay{path: path, entries: map[string]replayEntry{}}
	if path == "" {
		return r
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return r
	}
	if err != nil {
		r.err = err
		return r
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, replayBytes+1))
	if err == nil && len(raw) > replayBytes {
		err = errors.New("BPS replay file exceeds limit")
	}
	if err == nil {
		err = Decode(raw, &r.entries)
	}
	if r.entries == nil {
		r.entries = map[string]replayEntry{}
	}
	r.err = err
	return r
}

func (r *Replay) Ready() bool                    { r.mu.Lock(); defer r.mu.Unlock(); return r.err == nil }
func replayKey(owner, model, call string) string { return hash(owner, model, call) }
func (r *Replay) get(owner, model, call string) (replayEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[replayKey(owner, model, call)]
	if !ok || !entry.Expires.After(time.Now()) {
		return replayEntry{}, false
	}
	entry.Native = clone(entry.Native)
	entry.Client = clone(entry.Client)
	return entry, true
}

func (r *Replay) save(owner, model, account string, natives, clients []Object) error {
	if len(natives) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return fail(503, "replay_storage_unavailable", "BPS tool history storage is unavailable")
	}
	next := map[string]replayEntry{}
	now := time.Now()
	for key, entry := range r.entries {
		if entry.Expires.After(now) {
			next[key] = entry
		}
	}
	protected := map[string]bool{}
	for i, native := range natives {
		call := String(native["call_id"])
		if call == "" {
			return fail(502, "invalid_tool_call", "upstream tool call is missing its identity")
		}
		key := replayKey(owner, model, call)
		protected[key] = true
		if old, ok := next[key]; ok && (old.Account != account || string(encode(old.Native)) != string(encode(native))) {
			return fail(502, "tool_identity_conflict", "upstream reused a tool call ID for different work")
		}
		next[key] = replayEntry{Account: account, Native: clone(native), Client: clone(clients[i]), Expires: now.Add(24 * time.Hour)}
	}
	ordered := make([]string, 0, len(next))
	for key := range next {
		if !protected[key] {
			ordered = append(ordered, key)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return next[ordered[i]].Expires.Before(next[ordered[j]].Expires) })
	for len(next) > replayLimit || replaySize(next) > replayBytes {
		if len(ordered) == 0 {
			return fail(502, "tool_history_too_large", "tool history exceeds storage limit")
		}
		delete(next, ordered[0])
		ordered = ordered[1:]
	}
	if r.path != "" {
		if err := store.WriteJSONFile(r.path, next); err != nil {
			return fail(503, "replay_write_failed", "cannot persist BPS tool history; no tool calls were delivered")
		}
	}
	r.entries = next
	return nil
}

// AccountFor pins tool continuations to the original account, including after restart.
func (r *Replay) AccountFor(owner, model string, input any) (string, error) {
	if !r.Ready() {
		return "", fail(503, "replay_storage_unavailable", "BPS tool history storage is unavailable")
	}
	account := ""
	for _, raw := range list(input) {
		item := object(raw)
		kind := String(item["type"])
		if kind != "function_call" && kind != "custom_tool_call" && kind != "function_call_output" && kind != "custom_tool_call_output" {
			continue
		}
		entry, ok := r.get(owner, model, String(item["call_id"]))
		if !ok {
			return "", fail(409, "tool_history_missing", "BPS tool history expired or belongs to another caller; start a new conversation")
		}
		if account != "" && account != entry.Account {
			return "", fail(409, "mixed_account_history", "tool history belongs to different upstream accounts")
		}
		account = entry.Account
	}
	return account, nil
}

func replaySize(entries map[string]replayEntry) int {
	raw, _ := json.MarshalIndent(entries, "", "  ")
	return len(raw) + 1
}
