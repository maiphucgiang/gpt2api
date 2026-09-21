package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMailboxPreservesRetiredClaimsForRollback(t *testing.T) {
	var mailbox Mailbox
	if err := json.Unmarshal([]byte(`{"email":"test@example.test","openai_claimed":false,"retired_claimed":true}`), &mailbox); err != nil {
		t.Fatal(err)
	}
	if mailbox.OpenAIClaimed {
		t.Fatal("retired claim affected GPT availability")
	}
	raw, err := json.Marshal(mailbox)
	if err != nil || !strings.Contains(string(raw), `"retired_claimed":true`) {
		t.Fatal("retired claim was lost")
	}
	mailbox.OpenAIClaimed = true
	raw, err = json.Marshal(mailbox)
	if err != nil || !strings.Contains(string(raw), `"openai_claimed":true`) {
		t.Fatal("GPT claim was lost")
	}
}
