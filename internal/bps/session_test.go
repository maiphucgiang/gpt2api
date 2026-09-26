package bps

import (
	"path/filepath"
	"testing"
)

func TestEncryptedReasoningUsesOriginalAccountAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.json")
	replay := NewReplay(path)
	source := testSource()
	source["prompt_cache_key"] = "client-session"
	if err := replay.Bind("owner", "bsg/gpt-6-astra", "original", source); err != nil {
		t.Fatal(err)
	}
	next := clone(source)
	next["input"] = []any{message("user", "hello"), Object{"type": "reasoning", "encrypted_content": "opaque"}, message("assistant", "first answer"), message("user", "continue")}
	replay = NewReplay(path)
	account, err := replay.AccountForRequest("owner", "bsg/gpt-6-astra", next)
	if err != nil || account != "original" {
		t.Fatal("reasoning continuation lost original account", account, err)
	}
	if _, err = replay.AccountForRequest("another-owner", "bsg/gpt-6-astra", next); err == nil {
		t.Fatal("encrypted history crosses owners")
	}
	if err = replay.Bind("owner", "bsg/gpt-6-astra", "different", next); err == nil {
		t.Fatal("conversation rebound to different account")
	}
}

func TestUnsupportedGenerationParametersAreNotSent(t *testing.T) {
	source := testSource()
	source["max_output_tokens"] = 32
	source["temperature"] = 0.4
	source["top_p"] = 0.5
	prepared := prepareTest(t, source, NewReplay(""))
	for _, key := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := prepared.Body[key]; ok {
			t.Fatalf("unsupported wire parameter %s", key)
		}
	}
	for _, bad := range []any{-1, 1.5, "32"} {
		source["max_output_tokens"] = bad
		if _, err := Validate(source); err == nil {
			t.Fatal("invalid parameter accepted", bad)
		}
	}
}

func TestSessionIdentityIgnoresPrivateMessageIDs(t *testing.T) {
	plain := Object{"input": "hello"}
	history := Object{"input": []any{Object{"id": "different-every-turn", "role": "user", "content": "hello", "internal_chat_message_metadata_passthrough": Object{"turn_id": "random"}}, message("assistant", "answer"), message("user", "next")}}
	if conversationIdentity(plain) != conversationIdentity(history) {
		t.Fatal("private metadata changed session identity")
	}
}
