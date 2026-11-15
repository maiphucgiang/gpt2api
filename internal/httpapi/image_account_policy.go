package httpapi

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accountpolicy"
	"github.com/maiphucgiang/gpt2api/internal/accounts"
)

const highResolutionImageMaxEdge = 1920

func isHighResolutionImageSize(size string) bool {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(size)), "x")
	if len(parts) != 2 {
		return false
	}
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	return widthErr == nil && heightErr == nil && max(width, height) > highResolutionImageMaxEdge
}

// High-resolution requests prefer verified paid accounts and use other OpenAI
// accounts only when no paid account can currently be scheduled.
func (s *Server) reserveOpenAIImageAccount(ctx context.Context, excluded map[string]bool, size string) (*accounts.Lease, error) {
	pools := []string{"basic", "super", "heavy"}
	if !isHighResolutionImageSize(size) {
		return s.accountPool.ReserveMatchingLimit(ctx, pools, excluded, isOpenAIAccount, s.cfg.ImageAccountLimit)
	}

	now := time.Now()
	paid := func(account accounts.Account) bool {
		return isOpenAIAccount(account) && accountpolicy.SubscriptionEligible(account.Fields, now)
	}
	lease, err := s.accountPool.ReserveMatchingLimit(ctx, pools, excluded, paid, s.cfg.ImageAccountLimit)
	if !errors.Is(err, accounts.ErrUnavailable) {
		return lease, err
	}
	return s.accountPool.ReserveMatchingLimit(ctx, pools, excluded, isOpenAIAccount, s.cfg.ImageAccountLimit)
}
