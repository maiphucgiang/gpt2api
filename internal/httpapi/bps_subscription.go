package httpapi

import (
	"context"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
	"github.com/maiphucgiang/gpt2api/internal/store"
)

func (s *Server) refreshBPSSubscriptions(ctx context.Context, ref string) {
	items, err := s.store.AccountList()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		if !refreshableAccount(item) || !boolValue(item["bps_enabled"], true) || (ref != "" && store.AccountID(item) != ref) || !isOpenAIAccount(accounts.Account{Token: accountToken(item), Fields: item}) {
			continue
		}
		checked, _ := time.Parse(time.RFC3339, stringValue(item["subscription_checked_at"]))
		expires, _ := time.Parse(time.RFC3339, stringValue(item["subscription_expires_at"]))
		expiredSinceCheck := !expires.IsZero() && !expires.After(time.Now()) && checked.Before(expires)
		if !checked.After(time.Now().Add(time.Minute)) && checked.Add(time.Hour).After(time.Now()) && !expiredSinceCheck {
			continue
		}
		if retry, _ := time.Parse(time.RFC3339, stringValue(item["quota_refresh_retry_at"])); retry.After(time.Now()) {
			continue
		}
		_, _ = s.refreshAccountSnapshot(ctx, accountPublicRef(item))
	}
}
