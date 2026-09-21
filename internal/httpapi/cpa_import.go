package httpapi

import (
	"errors"
	"net/http"
	"strings"
)

var errUnsupportedCPACredential = errors.New("unsupported credential type: only Codex OAuth is accepted")

func isCodexCredential(value map[string]any) bool {
	kind := strings.ToLower(strings.TrimSpace(stringValue(value["type"])))
	provider := strings.ToLower(strings.TrimSpace(stringValue(value["provider"])))
	return (kind == "codex" || provider == "codex") && (kind == "" || kind == "codex") && (provider == "" || provider == "codex")
}

func normalizeCPACredential(value map[string]any) (map[string]any, error) {
	if !isCodexCredential(value) {
		return nil, errUnsupportedCPACredential
	}
	token := strings.TrimSpace(firstNonEmpty(stringValue(value["access_token"]), stringValue(value["accessToken"])))
	if token == "" {
		return nil, errors.New("missing access_token")
	}
	account := map[string]any{"access_token": token, "source_type": "codex", "credential_provider": "codex", "enabled": true, "status": "正常", "image_quota_unknown": true}
	for _, key := range []string{"refresh_token", "id_token", "account_id", "email", "expired", "last_refresh"} {
		if text := strings.TrimSpace(stringValue(value[key])); text != "" {
			account[key] = text
		}
	}
	if plan := strings.TrimSpace(stringValue(value["plan_type"])); plan != "" {
		account["type"] = plan
	}
	return account, nil
}

func (s *Server) importCPACredential(value map[string]any) (int, int, error) {
	account, err := normalizeCPACredential(value)
	if err != nil {
		return 0, 0, err
	}
	added, skipped, _, err := s.store.AddAccounts(nil, []map[string]any{account})
	return added, skipped, err
}

func (s *Server) cpaLocalImport(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed", "invalid_request_error")
		return
	}
	var body struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Accounts) == 0 || len(body.Accounts) > 500 {
		writeError(w, 400, "accounts must contain 1 to 500 entries", "invalid_request_error")
		return
	}
	added, skipped, unsupported := 0, 0, 0
	errorsOut := []map[string]any{}
	for index, item := range body.Accounts {
		n, d, err := s.importCPACredential(item)
		added += n
		skipped += d
		if errors.Is(err, errUnsupportedCPACredential) {
			unsupported++
			continue
		}
		if err != nil {
			errorsOut = append(errorsOut, map[string]any{"index": index, "error": err.Error()})
		}
	}
	writeJSON(w, 200, map[string]any{"added": added, "skipped": skipped, "unsupported": unsupported, "failed": len(errorsOut), "errors": errorsOut})
}
