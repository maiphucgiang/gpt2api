package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
	"github.com/maiphucgiang/gpt2api/internal/protocol"
)

// OpenAISearchModel is the catalog slug used by the ChatGPT Web search flow.
const OpenAISearchModel = "gpt-5-5"

const (
	openAISearchParentMessageID     = "client-created-root"
	openAISearchReportedSource      = "conversation_composer_web_icon"
	openAISearchDefaultPollTimeout  = 300 * time.Second
	openAISearchDefaultPollInterval = 3 * time.Second
)

var (
	openAISearchCitationRe    = regexp.MustCompile("\ue200cite\ue202([^\ue201]*)\ue201")
	openAISearchImageGroupRe  = regexp.MustCompile("\ue200image_group\ue202([^\ue201]*)\ue201")
	openAISearchInternalRe    = regexp.MustCompile("\ue200[a-zA-Z0-9_]+\ue202[^\ue201]*\ue201")
	openAISearchCitationIndex = regexp.MustCompile(`search(\d+)`)
	openAISearchURLRe         = regexp.MustCompile(`https?://[^\s"'<>）)\]}]+`)
	openAISearchLineSpacesRe  = regexp.MustCompile(`[ \t]+\n`)
	openAISearchBlankLinesRe  = regexp.MustCompile(`\n{3,}`)
)

type OpenAISearchSource struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	Snippet    string `json:"snippet"`
	SourceType string `json:"source_type"`
}

type OpenAISearchImageGroup struct {
	Queries     []string `json:"queries"`
	AspectRatio string   `json:"aspect_ratio,omitempty"`
	NumPerQuery int      `json:"num_per_query,omitempty"`
}

type OpenAISearchResult struct {
	ConversationID string
	Status         string
	Answer         string
	Model          string
	Sources        []OpenAISearchSource
	ImageGroups    []OpenAISearchImageGroup
}

func (c *OpenAIChat) Search(ctx context.Context, account accounts.Account, prompt, model string) (OpenAISearchResult, error) {
	if c == nil || c.Image == nil {
		return OpenAISearchResult{}, fmt.Errorf("OpenAI chat provider is not configured")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return OpenAISearchResult{}, fmt.Errorf("prompt is required")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = OpenAISearchModel
	}
	scripts, build, err := c.Image.bootstrap(ctx, account)
	if err != nil {
		return OpenAISearchResult{}, err
	}
	requirements, err := c.Image.chatRequirements(ctx, account, scripts, build)
	if err != nil {
		return OpenAISearchResult{}, err
	}
	conduit, err := c.prepareSearch(ctx, account, requirements, prompt, model)
	if err != nil {
		return OpenAISearchResult{}, err
	}
	conversationID, err := c.startSearch(ctx, account, requirements, conduit, prompt, model)
	if err != nil {
		return OpenAISearchResult{}, err
	}
	return c.waitSearchResult(ctx, account, conversationID)
}

func (c *OpenAIChat) prepareSearch(ctx context.Context, account accounts.Account, requirements openAIRequirements, prompt, model string) (string, error) {
	headers := c.Image.requirementHeaders(requirements)
	headers["X-Conduit-Token"] = "no-token"
	response, err := c.Image.do(ctx, http.MethodPost, "/backend-api/f/conversation/prepare", account, openAISearchPreparePayload(prompt, model), headers, false)
	if err != nil {
		return "", err
	}
	var value map[string]any
	if err := decodeOpenAIJSON(response, &value, "search conversation prepare"); err != nil {
		return "", err
	}
	conduit := stringValue(value["conduit_token"])
	if conduit == "" {
		return "", fmt.Errorf("search conversation prepare returned no conduit token")
	}
	return conduit, nil
}

func (c *OpenAIChat) startSearch(ctx context.Context, account accounts.Account, requirements openAIRequirements, conduit, prompt, model string) (string, error) {
	headers := c.Image.requirementHeaders(requirements)
	headers["X-Conduit-Token"] = conduit
	headers["Accept"] = "text/event-stream"
	response, err := c.Image.do(ctx, http.MethodPost, "/backend-api/f/conversation", account, openAISearchConversationPayload(prompt, model), headers, true)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	conversationID := ""
	var streamErr error
	err = scanOpenAISSE(response.Body, func(raw []byte) bool {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			return false
		}
		if object, ok := value.(map[string]any); ok {
			if detail := stringValue(object["detail"]); detail != "" {
				streamErr = &protocol.UpstreamError{Status: http.StatusBadRequest, Message: detail, Body: detail}
				return true
			}
		}
		if conversationID == "" {
			conversationID = nestedString(value, "conversation_id")
		}
		return false
	})
	if streamErr != nil {
		return conversationID, streamErr
	}
	if err != nil {
		return conversationID, err
	}
	if conversationID == "" {
		return "", fmt.Errorf("search conversation stream returned no conversation id")
	}
	return conversationID, nil
}

func (c *OpenAIChat) waitSearchResult(ctx context.Context, account accounts.Account, conversationID string) (OpenAISearchResult, error) {
	timeout := c.Image.RequestTimeout
	if timeout <= 0 {
		timeout = openAISearchDefaultPollTimeout
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	deadline := time.Now().Add(timeout)
	interval := openAISearchDefaultPollInterval
	if c.Image.pollInterval > 0 {
		interval = c.Image.pollInterval
	}
	var last OpenAISearchResult
	var lastErr error
	lastAnswer := ""
	stableHits := 0
	rateLimits := 0
	for {
		result, err := c.loadSearchConversation(ctx, account, conversationID)
		if err == nil {
			rateLimits = 0
			lastErr = nil
			last = result
			if openAISearchStatusDone(result.Status) {
				return result, nil
			}
			if result.Answer != "" {
				if result.Answer == lastAnswer {
					stableHits++
				} else {
					stableHits = 0
					lastAnswer = result.Answer
				}
				if stableHits >= 2 {
					return result, nil
				}
			}
		} else if !isRetryableOpenAISearchPollError(err) {
			return OpenAISearchResult{}, err
		} else {
			lastErr = err
			if openAIImageRateLimited(err) {
				rateLimits++
			} else {
				rateLimits = 0
			}
		}
		if !time.Now().Before(deadline) {
			break
		}
		wait := openAISearchPollWait(interval, rateLimits, lastErr)
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if wait <= 0 {
			break
		}
		if rateLimits > 0 {
			notifyOpenAIImageDuration(ctx, "retry_wait_ms", wait)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			if openAIImageRateLimited(lastErr) {
				return OpenAISearchResult{}, openAISearchPollTimeoutError(conversationID, last, lastErr, rateLimits)
			}
			return OpenAISearchResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return OpenAISearchResult{}, openAISearchPollTimeoutError(conversationID, last, lastErr, rateLimits)
}

func (c *OpenAIChat) loadSearchConversation(ctx context.Context, account accounts.Account, conversationID string) (OpenAISearchResult, error) {
	path := "/backend-api/conversation/" + url.PathEscape(conversationID)
	headers := map[string]string{
		"Accept":                "*/*",
		"Referer":               c.Image.BaseURL + "/c/" + conversationID,
		"X-OpenAI-Target-Route": "/backend-api/conversation/{conversation_id}",
	}
	response, err := c.Image.do(ctx, http.MethodGet, path, account, nil, headers, false)
	if err != nil {
		return OpenAISearchResult{}, err
	}
	var value any
	if err := decodeOpenAIJSON(response, &value, "search conversation"); err != nil {
		return OpenAISearchResult{}, err
	}
	return extractOpenAISearchResult(conversationID, value), nil
}

func openAISearchPollWait(interval time.Duration, rateLimits int, lastErr error) time.Duration {
	if rateLimits <= 0 {
		return interval
	}
	wait := openAIImagePollBackoff(interval, rateLimits)
	if retryAfter := accounts.RetryAfterDuration(lastErr); retryAfter > wait {
		return retryAfter
	}
	return wait
}

func openAISearchPollTimeoutError(conversationID string, last OpenAISearchResult, lastErr error, rateLimits int) error {
	if openAIImageRateLimited(lastErr) {
		return fmt.Errorf("timed out waiting for search result after %d rate-limited responses: %w", rateLimits, lastErr)
	}
	message := fmt.Sprintf("timed out waiting for search result: %s", conversationID)
	if lastErr != nil {
		message += " (last poll error: " + openAIImagePollErrorSummary(lastErr) + ")"
	} else if status := strings.TrimSpace(last.Status); status != "" {
		message += " (status: " + status + ")"
	}
	return &protocol.UpstreamError{Status: http.StatusGatewayTimeout, Message: message, Body: message}
}

func openAISearchPreparePayload(prompt, model string) map[string]any {
	return map[string]any{
		"action":                "next",
		"fork_from_shared_post": false,
		"parent_message_id":     openAISearchParentMessageID,
		"model":                 model,
		"client_prepare_state":  "success",
		"timezone_offset_min":   -480,
		"timezone":              "Asia/Shanghai",
		"conversation_mode":     map[string]any{"kind": "primary_assistant"},
		"system_hints":          []any{"search"},
		"partial_query": map[string]any{
			"id":      firstUUID(),
			"author":  map[string]any{"role": "user"},
			"content": map[string]any{"content_type": "text", "parts": []any{prompt}},
		},
		"supports_buffering":     true,
		"supported_encodings":    []any{"v1"},
		"client_contextual_info": map[string]any{"app_name": "chatgpt.com"},
	}
}

func openAISearchConversationPayload(prompt, model string) map[string]any {
	return map[string]any{
		"action": "next",
		"messages": []any{map[string]any{
			"id":          firstUUID(),
			"author":      map[string]any{"role": "user"},
			"create_time": float64(time.Now().UnixNano()) / 1e9,
			"content":     map[string]any{"content_type": "text", "parts": []any{prompt}},
			"metadata": map[string]any{
				"developer_mode_connector_ids": []any{},
				"selected_github_repos":        []any{},
				"selected_all_github_repos":    false,
				"system_hints":                 []any{"search"},
				"serialization_metadata":       map[string]any{"custom_symbol_offsets": []any{}},
			},
		}},
		"parent_message_id":                    openAISearchParentMessageID,
		"model":                                model,
		"client_prepare_state":                 "success",
		"timezone_offset_min":                  -480,
		"timezone":                             "Asia/Shanghai",
		"conversation_mode":                    map[string]any{"kind": "primary_assistant"},
		"enable_message_followups":             true,
		"system_hints":                         []any{},
		"supports_buffering":                   true,
		"supported_encodings":                  []any{"v1"},
		"force_use_search":                     true,
		"client_reported_search_source":        openAISearchReportedSource,
		"client_contextual_info":               map[string]any{"is_dark_mode": false, "time_since_loaded": 36, "page_height": 925, "page_width": 886, "pixel_ratio": 2, "screen_height": 1440, "screen_width": 2560, "app_name": "chatgpt.com"},
		"paragen_cot_summary_display_override": "allow",
		"force_parallel_switch":                "auto",
	}
}

func extractOpenAISearchResult(conversationID string, conversation any) OpenAISearchResult {
	result := OpenAISearchResult{ConversationID: strings.TrimSpace(conversationID), Sources: []OpenAISearchSource{}}
	message := latestOpenAISearchAssistantMessage(conversation)
	result.Answer = openAISearchMessageText(message)
	result.Status = openAISearchStatus(message)
	result.Model = openAISearchModelSlug(message)
	result.Sources = extractOpenAISearchSources(message)
	result.ImageGroups = collectOpenAISearchImageGroups(message)
	for _, rawURL := range openAISearchURLRe.FindAllString(result.Answer, -1) {
		cleaned := cleanOpenAISearchURL(rawURL)
		if cleaned == "" || openAISearchSourceHasURL(result.Sources, cleaned) {
			continue
		}
		result.Sources = append(result.Sources, OpenAISearchSource{URL: cleaned})
	}
	return result
}

func latestOpenAISearchAssistantMessage(conversation any) map[string]any {
	root, _ := conversation.(map[string]any)
	mapping, _ := root["mapping"].(map[string]any)
	var latest map[string]any
	latestTime := 0.0
	found := false
	for _, node := range mapping {
		object, _ := node.(map[string]any)
		message, _ := object["message"].(map[string]any)
		author, _ := message["author"].(map[string]any)
		if strings.ToLower(stringValue(author["role"])) != "assistant" {
			continue
		}
		created, _ := numberValue(message["create_time"])
		if !found || created >= latestTime {
			latest = message
			latestTime = created
			found = true
		}
	}
	if !found {
		return map[string]any{}
	}
	return latest
}

func openAISearchMessageText(message map[string]any) string {
	content := message["content"]
	parts := make([]string, 0)
	switch typed := content.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			parts = append(parts, text)
		}
	case map[string]any:
		if text := strings.TrimSpace(stringValue(typed["text"])); text != "" {
			parts = append(parts, text)
		}
		rawParts, _ := typed["parts"].([]any)
		for _, raw := range rawParts {
			switch part := raw.(type) {
			case string:
				if text := strings.TrimSpace(part); text != "" {
					parts = append(parts, text)
				}
			case map[string]any:
				for _, key := range []string{"text", "summary", "content"} {
					if text := strings.TrimSpace(stringValue(part[key])); text != "" {
						parts = append(parts, text)
					}
				}
			}
		}
	}
	return cleanOpenAISearchAnswer(strings.Join(parts, "\n"))
}

func cleanOpenAISearchAnswer(answer string) string {
	answer = openAISearchCitationRe.ReplaceAllStringFunc(answer, func(match string) string {
		sub := openAISearchCitationRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return ""
		}
		number := openAISearchCitationIndex.FindStringSubmatch(sub[1])
		if len(number) < 2 {
			return ""
		}
		index, err := strconv.Atoi(number[1])
		if err != nil {
			return ""
		}
		return fmt.Sprintf("[%d]", index+1)
	})
	answer = openAISearchImageGroupRe.ReplaceAllString(answer, "")
	answer = openAISearchInternalRe.ReplaceAllString(answer, "")
	answer = openAISearchLineSpacesRe.ReplaceAllString(answer, "\n")
	answer = openAISearchBlankLinesRe.ReplaceAllString(answer, "\n\n")
	return strings.TrimSpace(answer)
}

func extractOpenAISearchSources(payload any) []OpenAISearchSource {
	sources := make([]OpenAISearchSource, 0)
	for _, object := range walkOpenAISearchDicts(payload) {
		metadata, _ := object["metadata"].(map[string]any)
		rawURL := object["url"]
		if rawURL == nil {
			rawURL = object["link"]
		}
		if rawURL == nil {
			rawURL = object["source_url"]
		}
		if rawURL == nil && metadata != nil {
			rawURL = metadata["url"]
		}
		cleaned := cleanOpenAISearchURL(rawURL)
		if cleaned == "" || openAISearchSourceHasURL(sources, cleaned) {
			continue
		}
		sources = append(sources, OpenAISearchSource{
			Title:      firstNonEmptySearchString(object, "title", "name", "source"),
			URL:        cleaned,
			Snippet:    firstNonEmptySearchString(object, "snippet", "text", "description"),
			SourceType: firstNonEmptySearchString(object, "type", "source_type"),
		})
	}
	return sources
}

func collectOpenAISearchImageGroups(payload any) []OpenAISearchImageGroup {
	groups := make([]OpenAISearchImageGroup, 0)
	seen := map[string]bool{}
	var add func(any)
	var walk func(any)
	add = func(value any) {
		group, ok := normalizeOpenAISearchImageGroup(value)
		if !ok {
			return
		}
		raw, err := json.Marshal(group)
		if err != nil {
			return
		}
		key := string(raw)
		if seen[key] {
			return
		}
		seen[key] = true
		groups = append(groups, group)
	}
	walk = func(value any) {
		switch typed := value.(type) {
		case string:
			for _, match := range openAISearchImageGroupRe.FindAllStringSubmatch(typed, -1) {
				if len(match) > 1 {
					add(match[1])
				}
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			if imageGroup, ok := typed["image_group"]; ok {
				switch imageGroup.(type) {
				case map[string]any, string:
					add(imageGroup)
				}
				walk(imageGroup)
			}
			blockType := strings.ToLower(firstNonEmptySearchString(typed, "type", "name", "block_type"))
			if blockType == "image_group" {
				add(typed)
			} else if _, hasQuery := typed["query"]; hasQuery {
				if _, ok := typed["aspect_ratio"]; ok {
					add(typed)
				} else if _, ok := typed["num_per_query"]; ok {
					add(typed)
				}
			}
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(payload)
	return groups
}

func normalizeOpenAISearchImageGroup(payload any) (OpenAISearchImageGroup, bool) {
	if raw, ok := payload.(string); ok {
		var parsed any
		if json.Unmarshal([]byte(raw), &parsed) != nil {
			return OpenAISearchImageGroup{}, false
		}
		payload = parsed
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return OpenAISearchImageGroup{}, false
	}
	rawQueries := object["query"]
	if rawQueries == nil {
		rawQueries = object["queries"]
	}
	queryValues := make([]string, 0)
	switch typed := rawQueries.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			queryValues = append(queryValues, text)
		}
	case []any:
		for _, item := range typed {
			if text := stringValue(item); text != "" {
				queryValues = append(queryValues, text)
			}
		}
	}
	queries := make([]string, 0, len(queryValues))
	for _, query := range queryValues {
		if query = strings.TrimSpace(query); query != "" {
			queries = append(queries, query)
		}
		if len(queries) == 6 {
			break
		}
	}
	if len(queries) == 0 {
		return OpenAISearchImageGroup{}, false
	}
	group := OpenAISearchImageGroup{Queries: queries}
	if aspect := stringValue(object["aspect_ratio"]); aspect != "" {
		group.AspectRatio = aspect
	}
	if value, ok := numberValue(object["num_per_query"]); ok && int(value) > 0 {
		group.NumPerQuery = int(value)
	}
	return group, true
}

func openAISearchStatus(message map[string]any) string {
	metadata, _ := message["metadata"].(map[string]any)
	finish, _ := metadata["finish_details"].(map[string]any)
	if status := stringValue(finish["type"]); status != "" {
		return status
	}
	if status := stringValue(metadata["status"]); status != "" {
		return status
	}
	return strings.TrimSpace(nestedString(message, "status"))
}

func openAISearchModelSlug(message map[string]any) string {
	if slug := stringValue(message["model_slug"]); slug != "" {
		return slug
	}
	metadata, _ := message["metadata"].(map[string]any)
	return stringValue(metadata["model_slug"])
}

func openAISearchStatusDone(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "finished_successfully", "finished_partial_completion":
		return true
	default:
		return false
	}
}

func isRetryableOpenAISearchPollError(err error) bool {
	var upstream *protocol.UpstreamError
	if errors.As(err, &upstream) {
		switch upstream.Status {
		case http.StatusNotFound, http.StatusConflict, http.StatusLocked, http.StatusTooManyRequests,
			http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	return false
}

func walkOpenAISearchDicts(payload any) []map[string]any {
	switch typed := payload.(type) {
	case map[string]any:
		items := []map[string]any{typed}
		for _, value := range typed {
			items = append(items, walkOpenAISearchDicts(value)...)
		}
		return items
	case []any:
		items := make([]map[string]any, 0)
		for _, value := range typed {
			items = append(items, walkOpenAISearchDicts(value)...)
		}
		return items
	default:
		return nil
	}
}

func openAISearchSourceHasURL(sources []OpenAISearchSource, rawURL string) bool {
	for _, source := range sources {
		if source.URL == rawURL {
			return true
		}
	}
	return false
}

func cleanOpenAISearchURL(value any) string {
	if value == nil {
		return ""
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" || text == "<nil>" {
		return ""
	}
	return strings.TrimRight(text, ".,;，。；")
}

func firstNonEmptySearchString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := stringValue(object[key]); text != "" {
			return text
		}
	}
	return ""
}
