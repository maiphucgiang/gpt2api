package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accountpolicy"
	"github.com/maiphucgiang/gpt2api/internal/accounts"
	"github.com/maiphucgiang/gpt2api/internal/provider"
	"github.com/maiphucgiang/gpt2api/internal/store"
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
	return s.refreshAccountSnapshotExpected(ctx, ref, rotate, "")
}

func (s *Server) refreshAccountSnapshotExpected(ctx context.Context, ref string, rotate bool, expectedToken string) (map[string]any, error) {
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
				return s.refreshAccountSnapshotExpected(ctx, key, true, expectedToken)
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
	if expectedToken != "" && accountToken(account) != expectedToken {
		flight.account = cloneMap(account)
		return cloneMap(account), nil
	}
	client := s.openAIAccountClient()
	version := store.CredentialsOf(account)
	var checkpointErr error
	external := accountpolicy.External(account)
	synced := false
	sync := func(rejected bool) error {
		updated, syncErr := s.syncExternalCredentials(ctx, account, rejected)
		if syncErr != nil {
			return syncErr
		}
		account, version = updated, store.CredentialsOf(updated)
		flight.rotate, flight.account, synced = true, cloneMap(updated), true
		return nil
	}
	if external && (rotate || accountpolicy.Due(account, time.Now())) {
		if syncErr := sync(expectedToken != ""); syncErr != nil {
			flight.err = syncErr
			return nil, syncErr
		}
	}
	result, err := client.RefreshWithCheckpoint(ctx, account, rotate && !external, func(tokens provider.AccountRefreshResult) error {
		updated, err := s.store.SaveAccountCredentials(version, tokens.AccessToken, tokens.RefreshToken, tokens.IDToken)
		if updated != nil {
			account = updated
			version = store.CredentialsOf(updated)
			flight.rotate = true
			flight.account = cloneMap(updated)
		}
		checkpointErr = err
		return err
	})
	if external && !synced && errors.Is(err, provider.ErrInvalidAccessToken) {
		if syncErr := sync(true); syncErr != nil {
			flight.err = syncErr
			return nil, syncErr
		}
		result, err = client.RefreshWithCheckpoint(ctx, account, false, nil)
	}
	now := time.Now().UTC()
	if err != nil {
		if checkpointErr != nil {
			flight.err = checkpointErr
			return nil, flight.err
		}
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
		if _, updateErr := s.store.UpdateAccountIfCredentialsMatch(version, updates); updateErr != nil {
			flight.err = updateErr
			return nil, updateErr
		}
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
	flight.account, flight.err = s.store.CompleteAccountRefresh(version, fields)
	return cloneMap(flight.account), flight.err
}
