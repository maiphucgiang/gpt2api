package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accountpolicy"
	"github.com/maiphucgiang/gpt2api/internal/accounts"
	"github.com/maiphucgiang/gpt2api/internal/bps"
	"github.com/maiphucgiang/gpt2api/internal/model"
	"github.com/maiphucgiang/gpt2api/internal/protocol"
	"github.com/maiphucgiang/gpt2api/internal/store"
)

func (s *Server) bpsResponses(w http.ResponseWriter, r *http.Request, raw []byte) {
	var source bps.Object
	if bps.Decode(raw, &source) != nil {
		writeError(w, 400, "invalid BPS request", "invalid_request_error")
		return
	}
	s.serveBPS(w, r, source, "responses")
}

func (s *Server) serveBPS(w http.ResponseWriter, r *http.Request, source bps.Object, format string) {
	name := bps.String(source["model"])
	if !s.cfg.BPSEnabled || s.bpsClient == nil || !model.IsBPS(name) {
		writeError(w, 404, "BPS model is not enabled", "model_not_found")
		return
	}
	token := s.auth.APIKey(r)
	if token == "" {
		writeError(w, 401, "BPS requires an authenticated caller", "authentication_error")
		return
	}
	owner := s.authIdentity(r)
	if owner == "anonymous" {
		sum := sha256.Sum256([]byte(token))
		owner = fmt.Sprintf("key:%x", sum[:])
	}
	ignored := []string{}
	for _, field := range []string{"max_output_tokens", "temperature", "top_p"} {
		if source[field] != nil {
			ignored = append(ignored, field)
		}
	}
	if len(ignored) > 0 {
		w.Header().Set("X-BPS-Ignored-Parameters", strings.Join(ignored, ", "))
		w.Header().Set("Warning", `299 gpt2api "BPS does not enforce output length or sampling parameters"`)
	}
	tools, err := bps.Validate(source)
	if err != nil {
		writeBPSError(w, err)
		return
	}
	accountRef, err := s.bpsClient.Replay.AccountForRequest(owner, name, source)
	if err != nil {
		writeBPSError(w, err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	sink := newBPSOutput(w, source, format, cancel)
	var emit func(bps.Object) error
	if source["stream"] == true {
		emit = sink.event
	}
	result, err := s.runBPS(r, owner, accountRef, source, tools, emit)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		if sink.started {
			sink.failed(err)
		} else {
			writeBPSError(w, err)
		}
		return
	}
	if source["stream"] != true {
		writeJSON(w, 200, bpsCompletion(result, format))
		return
	}
	sink.finish()
}

func writeBPSError(w http.ResponseWriter, err error) {
	var upstream *bps.Error
	if errors.As(err, &upstream) {
		writeError(w, upstream.Status, upstream.Message, upstream.Code)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		writeError(w, 504, "BPS request timed out", "upstream_timeout")
		return
	}
	writeError(w, 502, "BPS request failed", "upstream_error")
}

func bpsCooldownKey(account accounts.Account, name string) string {
	sum := sha256.Sum256([]byte(account.Token))
	return fmt.Sprintf("%x:%s", sum[:], name)
}
func (s *Server) bpsAvailable(account accounts.Account, name, ref string) bool {
	if !isOpenAIAccount(account) || !boolValue(account.Fields["bps_enabled"], true) || !accountpolicy.SubscriptionEligible(account.Fields, time.Now()) || (ref != "" && store.AccountID(account.Fields) != ref) {
		return false
	}
	if _, _, err := bps.Credentials(account); err != nil {
		return false
	}
	// Web/image quota and backoff are separate from BPS model entitlements.
	status := strings.ToLower(stringValue(account.Fields["status"]))
	if status == "invalid" || status == "unauthorized" || stringValue(account.Fields["status_reason_code"]) == "auth_invalid" {
		return false
	}
	s.bpsMu.Lock()
	defer s.bpsMu.Unlock()
	return !s.bpsCooldowns[bpsCooldownKey(account, name)].After(time.Now())
}
func (s *Server) bpsFeedback(account accounts.Account, name string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	_, _ = s.store.RecordAccountResult(account.Token, store.AccountID(account.Fields), err == nil)
	if err == nil {
		return
	}
	status := bps.Status(err)
	delay := 5 * time.Second
	if status == 429 {
		delay = 30 * time.Second
	} else if status == 401 || status == 403 {
		delay = time.Minute
	}
	s.bpsMu.Lock()
	now := time.Now()
	for key, until := range s.bpsCooldowns {
		if !until.After(now) {
			delete(s.bpsCooldowns, key)
		}
	}
	s.bpsCooldowns[bpsCooldownKey(account, name)] = now.Add(delay)
	s.bpsMu.Unlock()
}

func (s *Server) runBPS(r *http.Request, owner, accountRef string, source bps.Object, tools *bps.Catalog, emit func(bps.Object) error) (bps.Object, error) {
	name := bps.String(source["model"])
	s.refreshBPSSubscriptions(r.Context(), accountRef)
	lease, err := s.accountPool.ReserveExisting(r.Context(), func(a accounts.Account) bool { return s.bpsAvailable(a, name, accountRef) }, 2)
	if err != nil {
		return nil, &bps.Error{Status: 503, Code: "bps_account_unavailable", Message: "no available BPS account for this model or continuation"}
	}
	defer s.accountPool.Release(lease)
	account := lease.Account
	refreshed := false
	refresh := func() error {
		if refreshed || (!accountpolicy.External(account.Fields) && stringValue(account.Fields["refresh_token"]) == "") {
			return &bps.Error{Status: 401, Code: "bps_auth_required", Message: "BPS access token needs renewal"}
		}
		refreshed = true
		updated, err := s.refreshAccountSnapshotExpected(r.Context(), accountPublicRef(account.Fields), true, account.Token)
		if err != nil {
			return &bps.Error{Status: 503, Code: "bps_refresh_failed", Message: "account credential refresh failed; new credentials, if issued, remain checkpointed"}
		}
		account = accounts.Account{Token: accountToken(updated), Pool: account.Pool, Fields: updated}
		if !s.bpsAvailable(account, name, accountRef) {
			return &bps.Error{Status: 403, Code: "bps_subscription_required", Message: "BPS requires a verified active subscription for this workspace"}
		}
		return nil
	}
	_, expires, _ := bps.Credentials(account)
	if !expires.IsZero() && !expires.After(time.Now().Add(30*time.Second)) {
		if err = refresh(); err != nil {
			return nil, err
		}
	}
	for {
		prepared, err := bps.Prepare(source, owner, store.AccountID(account.Fields), tools, s.bpsClient.Replay)
		if err != nil {
			return nil, err
		}
		if err := s.bpsClient.Replay.Bind(owner, name, store.AccountID(account.Fields), source); err != nil {
			return nil, err
		}
		s.enrichMonitorAccount(r, account)
		result, err := s.bpsClient.Run(r.Context(), account, prepared, emit)
		if r.Context().Err() == context.Canceled {
			err = context.Canceled
		}
		var attempt *bps.Error
		if err == nil || (errors.As(err, &attempt) && attempt.Attempted) {
			s.bpsFeedback(account, name, err)
		}
		var upstream *bps.Error
		if errors.As(err, &upstream) && upstream.Status == 401 && !upstream.Accepted && !refreshed && (accountpolicy.External(account.Fields) || stringValue(account.Fields["refresh_token"]) != "") {
			if err = refresh(); err != nil {
				return nil, err
			}
			continue
		}
		return result, err
	}
}

func (s *Server) bpsChatCompletions(w http.ResponseWriter, r *http.Request, request protocol.ChatRequest, stream bool) {
	source, err := bpsChatRequest(request)
	if err != nil {
		writeError(w, 400, err.Error(), "invalid_request_error")
		return
	}
	source["stream"] = stream
	s.serveBPS(w, r, source, "chat")
}

func (s *Server) bpsMessages(w http.ResponseWriter, r *http.Request, raw []byte) {
	var request bps.Object
	if bps.Decode(raw, &request) != nil {
		writeError(w, 400, "invalid Messages request", "invalid_request_error")
		return
	}
	source, err := bpsMessagesRequest(request)
	if err != nil {
		writeError(w, 400, err.Error(), "invalid_request_error")
		return
	}
	s.serveBPS(w, r, source, "messages")
}

func bpsJSON(value any) string { raw, _ := json.Marshal(value); return string(raw) }
