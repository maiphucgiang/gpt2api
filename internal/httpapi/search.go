package httpapi

import (
	"net/http"
	"strings"

	"github.com/maiphucgiang/gpt2api/internal/model"
	"github.com/maiphucgiang/gpt2api/internal/provider"
)

func (s *Server) searchAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAPI(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required", "invalid_request_error")
		return
	}
	modelID := strings.TrimSpace(body.Model)
	if modelID == "" {
		modelID = provider.OpenAISearchModel
	}
	route, ok := model.ResolveChat(modelID)
	if !ok || route.Image {
		writeError(w, http.StatusNotFound, "model not found", "invalid_request_error")
		return
	}
	excluded := map[string]bool{}
	var result provider.OpenAISearchResult
	var lastErr error
	for attempt := 0; attempt <= s.cfg.ChatMaxRetries; attempt++ {
		lease, err := s.accountPool.ReserveMatching(r.Context(), route.PoolCandidates, excluded, isOpenAIAccount)
		if err != nil {
			lastErr = err
			break
		}
		s.enrichMonitorAccount(r, lease.Account)
		err = s.runAccountBusiness(r.Context(), &lease.Account, func() error {
			var callErr error
			result, callErr = s.openAIChat.Search(r.Context(), lease.Account, prompt, modelID)
			return callErr
		}, func(error) bool { return true })
		s.accountPool.Release(lease)
		if err != nil {
			s.accountPool.Feedback(lease.Account, upstreamStatus(err), err)
			excluded[lease.Account.Token] = true
			lastErr = err
			if s.shouldRetry(upstreamStatus(err), attempt) {
				continue
			}
			break
		}
		s.accountPool.Feedback(lease.Account, http.StatusOK, nil)
		lastErr = nil
		break
	}
	if lastErr != nil {
		writeOpenAIChatError(w, lastErr)
		return
	}
	writeJSON(w, http.StatusOK, openAISearchAPIResponse(result))
}

func openAISearchAPIResponse(result provider.OpenAISearchResult) map[string]any {
	sources := make([]any, 0, len(result.Sources))
	for _, source := range result.Sources {
		sources = append(sources, map[string]any{
			"title":       source.Title,
			"url":         source.URL,
			"snippet":     source.Snippet,
			"source_type": source.SourceType,
		})
	}
	status := strings.TrimSpace(result.Status)
	if status == "" {
		status = "completed"
	}
	payload := map[string]any{
		"answer":  result.Answer,
		"sources": sources,
		"status":  status,
	}
	if id := strings.TrimSpace(result.ConversationID); id != "" {
		payload["conversation_id"] = id
	}
	if len(result.ImageGroups) > 0 {
		payload["image_groups"] = result.ImageGroups
	}
	if modelID := strings.TrimSpace(result.Model); modelID != "" {
		payload["model"] = modelID
	}
	return payload
}
