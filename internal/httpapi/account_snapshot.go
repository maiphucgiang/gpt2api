package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
	"github.com/maiphucgiang/gpt2api/internal/provider"
)

type accountRefreshFlight struct {
	rotate  bool
	done    chan struct{}
	account map[string]any
	err     error
}

// refreshAccountSnapshot coalesces manual and scheduled refreshes for one account.
func (s *Server) refreshAccountSnapshot(ctx context.Context, ref string) (map[string]any, error) {
	return s.refreshAccountSnapshotMode(ctx, ref, false)
}

func (s *Server) refreshAccountSnapshotMode(ctx context.Context, ref string, rotate bool) (map[string]any, error) {
	account, err := s.accountByRef(ref)
	if err != nil {
		return nil, err
	}
	key := accountPublicRef(account)
	s.accountRefreshMu.Lock()
	if flight := s.accountRefreshFlights[key]; flight != nil {
		s.accountRefreshMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-flight.done:
			if rotate && !flight.rotate && flight.err == nil {
				return s.refreshAccountSnapshotMode(ctx, key, true)
			}
			return cloneMap(flight.account), flight.err
		}
	}
	flight := &accountRefreshFlight{done: make(chan struct{}), rotate: rotate}
	if s.accountRefreshFlights == nil {
		s.accountRefreshFlights = map[string]*accountRefreshFlight{}
	}
	s.accountRefreshFlights[key] = flight
	s.accountRefreshMu.Unlock()
	defer func() {
		s.accountRefreshMu.Lock()
		delete(s.accountRefreshFlights, key)
		close(flight.done)
		s.accountRefreshMu.Unlock()
	}()
	account, err = s.accountByRef(key)
	if err != nil {
		flight.err = err
		return nil, err
	}
	if !isOpenAIAccount(accounts.Account{Token: accountToken(account), Fields: account}) {
		flight.err = errors.New("not a GPT account")
		return nil, flight.err
	}
	client := s.openAIAccountClient()
	var result provider.AccountRefreshResult
	if rotate {
		result, err = client.RefreshAccessToken(ctx, account)
	} else {
		result, err = client.RefreshAccount(ctx, account)
	}
	now := time.Now().UTC()
	if err != nil {
		if ctx.Err() != nil {
			flight.err = ctx.Err()
			return nil, flight.err
		}
		updates := accountRefreshFailureUpdates(err)
		failures := min(intValue(account["quota_refresh_failures"])+1, 7)
		updates["quota_refresh_failures"] = failures
		updates["quota_refresh_retry_at"] = now.Add(time.Minute * time.Duration(1<<uint(failures-1))).Format(time.RFC3339)
		updates["last_quota_refresh_error"] = safeRefreshError(err)
		if errors.Is(err, provider.ErrInvalidAccessToken) {
			updates["survival_alive"] = false
		}
		_, _, _ = s.store.UpdateAccount(accountToken(account), updates)
		flight.err = err
		return nil, err
	}
	fields := cloneMap(result.Fields)
	plan := firstNonEmpty(stringValue(fields["type"]), "free")
	fields["last_quota_refresh_at"] = now.Format(time.RFC3339)
	fields["last_quota_refresh_error"] = nil
	fields["quota_refresh_retry_at"] = nil
	fields["quota_refresh_failures"] = 0
	fields["survival_status"] = plan
	fields["survival_last_probe_status"] = plan
	fields["survival_last_checked_at"] = now.Format(time.RFC3339)
	fields["survival_alive"] = true
	fields["survival_plan_type"] = plan
	fields["survival_check_error"] = nil
	flight.account, _, flight.err = s.store.RotateAccountTokens(accountToken(account), result.AccessToken, result.RefreshToken, result.IDToken, fields)
	if flight.err == nil && flight.account == nil {
		flight.err = errors.New("account credentials changed during refresh; retry with current credentials")
	}
	return cloneMap(flight.account), flight.err
}
