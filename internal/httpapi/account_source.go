package httpapi

import (
	"net/http"

	"github.com/maiphucgiang/gpt2api/internal/accountpolicy"
	"github.com/maiphucgiang/gpt2api/internal/store"
)

func (s *Server) bindAccountSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed", "invalid_request_error")
		return
	}
	var body struct {
		AccountRef string `json:"account_ref"`
		Owner      string `json:"credential_owner"`
		Source     string `json:"credential_source_id"`
		Remote     string `json:"credential_remote_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	account, err := s.accountByRef(body.AccountRef)
	if err != nil || body.Source == "" || body.Remote == "" || (body.Owner != "cpa" && body.Owner != "sub2api") {
		writeError(w, 400, "a saved account and an external source binding are required", "invalid_request_error")
		return
	}
	version := store.CredentialsOf(account)
	var payload map[string]any
	if body.Owner == "cpa" {
		pool, found := s.external.getCPA(body.Source)
		if !found {
			writeError(w, 404, "CPA source not found", "not_found")
			return
		}
		payload, err = s.readCPACredential(r.Context(), pool, body.Remote)
	} else {
		server, found := s.external.getSub(body.Source)
		if !found {
			writeError(w, 404, "Sub2API source not found", "not_found")
			return
		}
		var headers map[string]string
		headers, err = subHeadersContext(r.Context(), server)
		if err == nil {
			payload, err = s.readSubCredential(r.Context(), server, body.Remote, headers)
		}
	}
	if err != nil {
		writeError(w, 502, "source credential could not be verified", "external_source_unavailable")
		return
	}
	oldSubject, oldWorkspace := accountpolicy.Identity(account)
	if accountToken(account) != accountToken(payload) && (oldSubject == "" || oldWorkspace == "") {
		writeError(w, 409, "source binding requires matching credentials or a verified subject and workspace", "credential_identity_conflict")
		return
	}
	bound := cloneMap(account)
	bound["credential_owner"], bound["credential_source_id"], bound["credential_remote_id"] = body.Owner, body.Source, body.Remote
	updated, err := s.store.SaveExternalCredentials(version, bound, payload)
	if err != nil {
		writeError(w, 409, "account or source identity changed; inspect the binding before retrying", "credential_identity_conflict")
		return
	}
	select {
	case s.survivalWake <- struct{}{}:
	default:
	}
	writeJSON(w, 200, map[string]any{"account": accountForAPI(updated)})
}
