package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accountpolicy"
	"github.com/maiphucgiang/gpt2api/internal/accounts"
	"github.com/maiphucgiang/gpt2api/internal/store"
)

func (s *Server) prepareBusinessCredentials(ctx context.Context, account *accounts.Account) error {
	if !accountpolicy.External(account.Fields) || !accountpolicy.Due(account.Fields, time.Now()) {
		return nil
	}
	updated, err := s.refreshAccountSnapshotMode(ctx, accountPublicRef(account.Fields), true)
	if err != nil {
		// A failed source request must not retire an access token that still works.
		if expiry := accountpolicy.Expiry(account.Fields); !expiry.IsZero() && expiry.After(time.Now().Add(30*time.Second)) {
			return nil
		}
		return fmt.Errorf("%w: source synchronization is pending", accountpolicy.ErrCredentialPending)
	}
	*account = accounts.Account{Token: accountToken(updated), Pool: account.Pool, Fields: updated}
	return nil
}

// runAccountBusiness retries only an unaccepted 401, preserving per-attempt statistics.
func (s *Server) runAccountBusiness(ctx context.Context, account *accounts.Account, call func() error, canRetry func(error) bool) error {
	if err := s.prepareBusinessCredentials(ctx, account); err != nil {
		return err
	}
	err := call()
	if !accountpolicy.External(account.Fields) || upstreamStatus(err) != http.StatusUnauthorized || !canRetry(err) || errors.Is(err, context.Canceled) {
		return err
	}
	old := *account
	updated, syncErr := s.refreshAccountSnapshotExpected(ctx, accountPublicRef(old.Fields), true, old.Token)
	if syncErr != nil || accountToken(updated) == old.Token {
		return err
	}
	_, _ = s.store.RecordAccountResult(old.Token, store.AccountID(old.Fields), false)
	*account = accounts.Account{Token: accountToken(updated), Pool: old.Pool, Fields: updated}
	return call()
}
