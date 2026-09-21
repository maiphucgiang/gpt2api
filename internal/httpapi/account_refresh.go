package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/provider"
	proxyruntime "github.com/maiphucgiang/gpt2api/internal/proxy"
)

type accountRefreshProgress struct {
	Total        int            `json:"total"`
	Processed    int            `json:"processed"`
	Done         bool           `json:"done"`
	Error        string         `json:"error,omitempty"`
	StatusCounts map[string]int `json:"status_counts"`
	TotalQuota   int            `json:"total_quota"`
	Result       map[string]any `json:"result,omitempty"`
}

func (s *Server) accountAccessTokenRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
		return
	}
	var body struct {
		AccessTokens []string `json:"access_tokens"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	refs := uniqueAccountRefs(body.AccessTokens)
	if len(refs) == 0 {
		writeError(w, http.StatusBadRequest, "access_tokens is required", "invalid_request_error")
		return
	}
	updated := 0
	errorsOut := make([]map[string]any, 0)
	for _, ref := range refs {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		_, refreshErr := s.refreshAccountSnapshotMode(ctx, ref, true)
		cancel()
		if refreshErr != nil {
			errorsOut = append(errorsOut, map[string]any{"token": tokenPreview(ref), "error": safeRefreshError(refreshErr)})
			continue
		}
		updated++
	}
	items, _ := s.store.AccountList()
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated, "success_count": updated, "errors": errorsOut, "items": accountsForAPI(items)})
}

func (s *Server) accountRefreshStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
		return
	}
	var body struct {
		AccessTokens []string `json:"access_tokens"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	refs := uniqueAccountRefs(body.AccessTokens)
	if len(refs) == 0 {
		items, err := s.store.AccountList()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return
		}
		for _, item := range items {
			if ref := accountPublicRef(item); ref != "" && accountToken(item) != "" {
				refs = append(refs, ref)
			}
		}
		refs = uniqueAccountRefs(refs)
	}
	if len(refs) == 0 {
		writeError(w, http.StatusBadRequest, "access_tokens is required", "invalid_request_error")
		return
	}

	progressID := newChatID()
	s.refreshMu.Lock()
	s.refreshProgress[progressID] = &accountRefreshProgress{
		Total:        len(refs),
		StatusCounts: map[string]int{"正常": 0, "限流": 0, "异常": 0, "禁用": 0},
	}
	s.refreshMu.Unlock()

	s.survivalMu.Lock()
	if s.backgroundContext.Err() != nil {
		s.survivalMu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "server is shutting down", "server_error")
		return
	}
	s.backgroundWorkers.Add(1)
	s.survivalMu.Unlock()
	go func() {
		defer s.backgroundWorkers.Done()
		s.runAccountRefresh(progressID, refs)
	}()
	writeJSON(w, http.StatusOK, map[string]any{"progress_id": progressID})
}

func (s *Server) accountRefreshProgressAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/accounts/refresh/progress/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "progress not found", "not_found")
		return
	}
	s.refreshMu.RLock()
	progress, ok := s.refreshProgress[id]
	if ok {
		copy := *progress
		copy.StatusCounts = cloneIntMap(progress.StatusCounts)
		copy.Result = cloneMap(progress.Result)
		progress = &copy
	}
	s.refreshMu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "progress not found", "not_found")
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

func (s *Server) runAccountRefresh(progressID string, refs []string) {
	ctx := s.backgroundContext
	var wg sync.WaitGroup
	sem := make(chan struct{}, accountRefreshConcurrency())
	for _, ref := range refs {
		ref := ref
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.refreshOneAccount(ctx, progressID, ref)
		}()
	}
	wg.Wait()
	items, err := s.store.AccountList()
	result := map[string]any{"refreshed": 0, "errors": []any{}, "items": accountsForAPI(items)}
	if err != nil {
		s.finishAccountRefresh(progressID, nil, err.Error())
		return
	}
	s.refreshMu.Lock()
	if progress := s.refreshProgress[progressID]; progress != nil {
		if progress.Result != nil {
			result = progress.Result
		}
		result["items"] = accountsForAPI(items)
		progress.Result = result
		progress.Done = true
	}
	s.refreshMu.Unlock()
}

func (s *Server) refreshOneAccount(parent context.Context, progressID, ref string) {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	updated, err := s.refreshAccountSnapshot(ctx, ref)
	if err != nil {
		s.recordRefreshError(progressID, ref, err)
		return
	}
	s.recordRefreshSuccess(progressID)
	s.updateRefreshStatus(progressID, updated)
}

func (s *Server) openAIAccountClient() *provider.OpenAIAccountClient {
	client := s.requestClient
	if client == nil {
		client = &http.Client{
			Transport: proxyruntime.NewTransport(http.DefaultTransport),
			Timeout:   45 * time.Second,
		}
	}
	return provider.NewOpenAIAccountClient(
		s.cfg.OpenAIBaseURL,
		s.cfg.OpenAIOAuthURL,
		client,
		s.proxyManager,
		provider.ClearanceConfig{URL: s.cfg.FlareSolverrURL, Enabled: s.cfg.ClearanceEnabled, Timeout: s.cfg.ClearanceTimeout},
	)
}

func (s *Server) recordRefreshSuccess(progressID string) {
	s.refreshMu.Lock()
	if progress := s.refreshProgress[progressID]; progress != nil {
		progress.Processed++
		if progress.Result == nil {
			progress.Result = map[string]any{"refreshed": 0, "errors": []any{}}
		}
		progress.Result["refreshed"] = intValue(progress.Result["refreshed"]) + 1
	}
	s.refreshMu.Unlock()
}

func (s *Server) recordRefreshError(progressID, ref string, err error) {
	s.refreshMu.Lock()
	if progress := s.refreshProgress[progressID]; progress != nil {
		progress.Processed++
		if progress.Result == nil {
			progress.Result = map[string]any{}
		}
		errorsValue, _ := progress.Result["errors"].([]any)
		errorsValue = append(errorsValue, map[string]any{"token": tokenPreview(ref), "error": safeRefreshError(err)})
		progress.Result["errors"] = errorsValue
	}
	s.refreshMu.Unlock()
	// Update status counts from the persisted account after the error marker is
	// written, so progress reflects what the next account selection sees.
	if account, lookupErr := s.accountByRef(ref); lookupErr == nil {
		s.updateRefreshStatus(progressID, account)
	}
}

func accountRefreshConcurrency() int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("GO_ACCOUNT_REFRESH_CONCURRENCY")))
	if err != nil || value < 1 {
		return 3
	}
	if value > 10 {
		return 10
	}
	return value
}

func (s *Server) updateRefreshStatus(progressID string, account map[string]any) {
	category := accountStatusCategory(account)
	label := map[string]string{"normal": "正常", "limited": "限流", "abnormal": "异常", "disabled": "禁用"}[category]
	if label == "" {
		label = "异常"
	}
	s.refreshMu.Lock()
	if progress := s.refreshProgress[progressID]; progress != nil {
		progress.StatusCounts[label]++
		progress.TotalQuota += intValue(account["quota"])
	}
	s.refreshMu.Unlock()
}

func (s *Server) finishAccountRefresh(progressID string, result map[string]any, message string) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if progress := s.refreshProgress[progressID]; progress != nil {
		progress.Done = true
		progress.Error = message
		progress.Result = result
	}
}

func (s *Server) accountByRef(ref string) (map[string]any, error) {
	items, err := s.store.AccountList()
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if accountRefMatches(item, ref) {
			return item, nil
		}
	}
	return nil, errors.New("account not found")
}

func accountRefreshFailureUpdates(err error) map[string]any {
	if err == nil {
		return nil
	}
	message := safeRefreshError(err)
	lower := strings.ToLower(message)
	now := time.Now().UTC().Format(time.RFC3339)
	if errors.Is(err, provider.ErrInvalidAccessToken) ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "http 401") ||
		strings.Contains(lower, "invalid_grant") ||
		strings.Contains(lower, "invalid access token") ||
		strings.Contains(lower, "access token invalid") {
		return map[string]any{
			"status":                "异常",
			"last_refresh_error":    message,
			"last_refresh_error_at": now,
			"last_error_kind":       "auth_invalid",
			"last_error_status":     401,
			"status_reason_code":    "account_invalid",
		}
	}
	if strings.Contains(lower, "http 429") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "额度") {
		return map[string]any{
			"status":                "限流",
			"last_refresh_error":    message,
			"last_refresh_error_at": now,
			"last_error_kind":       "quota_exhausted",
			"status_reason_code":    "image_quota_exhausted",
		}
	}
	return map[string]any{
		"last_refresh_warning":    message,
		"last_refresh_warning_at": now,
		"last_refresh_error":      nil,
		"last_refresh_error_at":   nil,
		"last_error_kind":         nil,
		"last_error_status":       nil,
		"last_error_message":      nil,
		"status_reason_code":      nil,
	}
}

func cloneIntMap(value map[string]int) map[string]int {
	copy := map[string]int{}
	for key, item := range value {
		copy[key] = item
	}
	return copy
}

func safeRefreshError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 300 {
		message = message[:300]
	}
	return message
}
