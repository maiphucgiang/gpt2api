package httpapi

import (
	"encoding/base64"
	"testing"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
)

func TestGPTCredentialsRemainUsableAcrossImportSources(t *testing.T) {
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://auth.openai.com/"}`)) + ".signature"
	for _, source := range []string{"", "web", "manual", "session_json", "access_token", "cpa_json"} {
		if !isOpenAIAccount(accounts.Account{Token: token, Fields: map[string]any{"source_type": source}}) {
			t.Errorf("GPT credential excluded for source %q", source)
		}
	}
	if isOpenAIAccount(accounts.Account{Token: token, Fields: map[string]any{"credential_provider": "unsupported"}}) {
		t.Fatal("explicit foreign provider accepted")
	}
	foreign := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://foreign.example"}`)) + ".signature"
	if isOpenAIAccount(accounts.Account{Token: foreign, Fields: map[string]any{"source_type": "manual"}}) {
		t.Fatal("foreign issuer accepted")
	}
}
