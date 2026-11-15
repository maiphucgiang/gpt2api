package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
	"github.com/maiphucgiang/gpt2api/internal/protocol"
	proxyruntime "github.com/maiphucgiang/gpt2api/internal/proxy"
)

func TestOpenAISearchPayloadEnablesUpstreamSearch(t *testing.T) {
	prepare := openAISearchPreparePayload("latest rust release", OpenAISearchModel)
	hints, _ := prepare["system_hints"].([]any)
	if len(hints) != 1 || hints[0] != "search" {
		t.Fatalf("prepare system_hints: %#v", prepare["system_hints"])
	}
	if prepare["model"] != OpenAISearchModel {
		t.Fatalf("prepare model: %#v", prepare["model"])
	}
	if _, ok := prepare["thinking_effort"]; ok {
		t.Fatal("search prepare must not send thinking_effort")
	}

	conversation := openAISearchConversationPayload("latest rust release", OpenAISearchModel)
	if conversation["force_use_search"] != true {
		t.Fatalf("force_use_search: %#v", conversation["force_use_search"])
	}
	if conversation["client_reported_search_source"] != openAISearchReportedSource {
		t.Fatalf("search source: %#v", conversation["client_reported_search_source"])
	}
	if conversation["model"] != OpenAISearchModel {
		t.Fatalf("conversation model: %#v", conversation["model"])
	}
	topHints, _ := conversation["system_hints"].([]any)
	if len(topHints) != 0 {
		t.Fatalf("top-level system_hints must stay empty: %#v", conversation["system_hints"])
	}
	messages, _ := conversation["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages: %#v", conversation["messages"])
	}
	message, _ := messages[0].(map[string]any)
	metadata, _ := message["metadata"].(map[string]any)
	messageHints, _ := metadata["system_hints"].([]any)
	if len(messageHints) != 1 || messageHints[0] != "search" {
		t.Fatalf("message system_hints: %#v", metadata["system_hints"])
	}
	if _, ok := conversation["thinking_effort"]; ok {
		t.Fatal("search conversation must not send thinking_effort")
	}
}

func TestExtractOpenAISearchResultParsesCitationsSessionAndImageGroups(t *testing.T) {
	answer := "Rust 1.80 is out\ue200cite\ue202search0\ue201.\ue200image_group\ue202{\"query\":[\"rust logo\"],\"aspect_ratio\":\"1:1\",\"num_per_query\":2}\ue201 See https://blog.rust-lang.org/2024/extra."
	result := extractOpenAISearchResult("conversation-search", map[string]any{
		"mapping": map[string]any{
			"user": map[string]any{"message": map[string]any{"author": map[string]any{"role": "user"}, "create_time": 1.0}},
			"assistant": map[string]any{"message": map[string]any{
				"author":      map[string]any{"role": "assistant"},
				"create_time": 2.0,
				"status":      "in_progress",
				"metadata": map[string]any{
					"model_slug":     "gpt-5-5",
					"finish_details": map[string]any{"type": "finished_successfully"},
					"content_references": []any{
						map[string]any{
							"title": "Rust Blog", "url": "https://blog.rust-lang.org/2024/", "snippet": "Rust 1.80", "type": "webpage",
						},
					},
				},
				"content": map[string]any{"content_type": "text", "parts": []any{answer}},
			}},
		},
	})
	if result.ConversationID != "conversation-search" {
		t.Fatalf("conversation id: %#v", result.ConversationID)
	}
	if result.Status != "finished_successfully" {
		t.Fatalf("status: %#v", result.Status)
	}
	if result.Model != "gpt-5-5" {
		t.Fatalf("model: %#v", result.Model)
	}
	if !strings.Contains(result.Answer, "Rust 1.80 is out[1].") {
		t.Fatalf("answer citations were not rewritten: %q", result.Answer)
	}
	if strings.Contains(result.Answer, "image_group") || strings.Contains(result.Answer, "\ue200") {
		t.Fatalf("answer still contains search markers: %q", result.Answer)
	}
	if len(result.Sources) != 2 {
		t.Fatalf("sources: %#v", result.Sources)
	}
	if result.Sources[0].URL != "https://blog.rust-lang.org/2024/" || result.Sources[0].Title != "Rust Blog" {
		t.Fatalf("structured source: %#v", result.Sources[0])
	}
	if result.Sources[1].URL != "https://blog.rust-lang.org/2024/extra" {
		t.Fatalf("answer url was not cleaned: %#v", result.Sources[1])
	}
	if len(result.ImageGroups) != 1 || result.ImageGroups[0].Queries[0] != "rust logo" || result.ImageGroups[0].AspectRatio != "1:1" || result.ImageGroups[0].NumPerQuery != 2 {
		t.Fatalf("image groups: %#v", result.ImageGroups)
	}
}

func TestExtractOpenAISearchResultEmptyAssistantOmitsModelAndGroups(t *testing.T) {
	result := extractOpenAISearchResult("conversation-empty", map[string]any{
		"mapping": map[string]any{
			"assistant": map[string]any{"message": map[string]any{
				"author":      map[string]any{"role": "assistant"},
				"create_time": 1.0,
				"metadata":    map[string]any{"finish_details": map[string]any{"type": "finished_successfully"}},
				"content":     map[string]any{"content_type": "text", "parts": []any{""}},
			}},
		},
	})
	if result.ConversationID != "conversation-empty" {
		t.Fatalf("conversation id: %#v", result.ConversationID)
	}
	if result.Answer != "" {
		t.Fatalf("answer: %#v", result.Answer)
	}
	if result.Model != "" {
		t.Fatalf("empty upstream model must stay empty: %#v", result.Model)
	}
	if len(result.Sources) != 0 {
		t.Fatalf("sources: %#v", result.Sources)
	}
	if len(result.ImageGroups) != 0 {
		t.Fatalf("image groups: %#v", result.ImageGroups)
	}
}

func TestExtractOpenAISearchResultUsesLatestAssistantMessage(t *testing.T) {
	result := extractOpenAISearchResult("conversation-latest", map[string]any{
		"mapping": map[string]any{
			"old": map[string]any{"message": map[string]any{
				"author":      map[string]any{"role": "assistant"},
				"create_time": 1.0,
				"metadata":    map[string]any{"model_slug": "gpt-5", "finish_details": map[string]any{"type": "finished_successfully"}},
				"content":     map[string]any{"parts": []any{"old answer https://old.example/"}},
			}},
			"new": map[string]any{"message": map[string]any{
				"author":      map[string]any{"role": "assistant"},
				"create_time": 3.0,
				"metadata": map[string]any{
					"model_slug":     "gpt-5-5",
					"finish_details": map[string]any{"type": "finished_successfully"},
					"content_references": []any{
						map[string]any{"title": "New", "url": "https://new.example/", "snippet": "now"},
					},
				},
				"content": map[string]any{"parts": []any{"new answer"}},
			}},
		},
	})
	if result.Answer != "new answer" || result.Model != "gpt-5-5" {
		t.Fatalf("did not keep the latest assistant message: %#v", result)
	}
	if len(result.Sources) != 1 || result.Sources[0].URL != "https://new.example/" {
		t.Fatalf("sources: %#v", result.Sources)
	}
}

func TestOpenAISearchPollWaitHonorsRetryAfterAndBackoff(t *testing.T) {
	interval := 5 * time.Millisecond
	limited := &protocol.UpstreamError{Status: http.StatusTooManyRequests, Message: "slow down", RetryAfter: 40 * time.Millisecond}
	if got := openAISearchPollWait(interval, 1, limited); got < 40*time.Millisecond {
		t.Fatalf("Retry-After was not honored: %v", got)
	}
	backoff := openAISearchPollWait(interval, 2, &protocol.UpstreamError{Status: http.StatusTooManyRequests, Message: "slow down"})
	base := interval * time.Duration(1<<1)
	if backoff < base || backoff > min(time.Minute, base+base/4) {
		t.Fatalf("capped backoff not applied: %v base %v", backoff, base)
	}
	if got := openAISearchPollWait(interval, 0, nil); got != interval {
		t.Fatalf("normal interval: %v", got)
	}
}

func TestOpenAISearchPollingRateLimitBackoffAndRecovery(t *testing.T) {
	var polls atomic.Int32
	chat := newSearchPollChat(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/conversation-search" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if polls.Add(1) <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"detail":"slow down"}`)
			return
		}
		writeSearchPollConversation(w, "finished_successfully", "search done", []any{
			map[string]any{"title": "Rust Blog", "url": "https://blog.rust-lang.org/2024/"},
		})
	}, 5*time.Second, time.Millisecond)

	var retryWaits []time.Duration
	ctx := WithOpenAIImageStage(context.Background(), func(metric string, elapsed time.Duration) {
		if metric == "retry_wait_ms" {
			retryWaits = append(retryWaits, elapsed)
		}
	})
	result, err := chat.waitSearchResult(ctx, accounts.Account{}, "conversation-search")
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != "search done" || result.Status != "finished_successfully" || polls.Load() != 3 {
		t.Fatalf("unexpected recovery: %#v polls=%d", result, polls.Load())
	}
	if len(retryWaits) != 2 {
		t.Fatalf("rate-limit backoff was not observed: %v", retryWaits)
	}
	for _, wait := range retryWaits {
		if wait < time.Second {
			t.Fatalf("Retry-After was not applied: %v", retryWaits)
		}
	}
}

func TestOpenAISearchPollingSustainedRateLimitTimesOut(t *testing.T) {
	var polls atomic.Int32
	chat := newSearchPollChat(t, func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"detail":"slow down"}`)
	}, 80*time.Millisecond, time.Millisecond)

	result, err := chat.waitSearchResult(context.Background(), accounts.Account{}, "conversation-search")
	var upstream *protocol.UpstreamError
	if err == nil || !errors.As(err, &upstream) || upstream.Status != http.StatusTooManyRequests {
		t.Fatalf("expected wrapped 429 timeout, got result=%#v err=%v", result, err)
	}
	if !strings.Contains(err.Error(), "timed out waiting for search result") {
		t.Fatalf("unrecognized timeout: %v", err)
	}
	if result.Answer != "" || result.ConversationID != "" || len(result.Sources) != 0 {
		t.Fatalf("timeout returned an incomplete result: %#v", result)
	}
	if polls.Load() < 1 {
		t.Fatal("search poll never contacted upstream")
	}
}

func TestOpenAISearchPollingIncompleteTimesOut(t *testing.T) {
	var polls atomic.Int32
	chat := newSearchPollChat(t, func(w http.ResponseWriter, r *http.Request) {
		n := polls.Add(1)
		writeSearchPollConversation(w, "in_progress", fmt.Sprintf("partial-%d", n), []any{
			map[string]any{"title": "Draft", "url": "https://example.test/draft"},
		})
	}, 80*time.Millisecond, time.Millisecond)

	result, err := chat.waitSearchResult(context.Background(), accounts.Account{}, "conversation-search")
	var upstream *protocol.UpstreamError
	if err == nil || !errors.As(err, &upstream) || upstream.Status != http.StatusGatewayTimeout {
		t.Fatalf("expected search timeout, got result=%#v err=%v", result, err)
	}
	if !strings.Contains(err.Error(), "timed out waiting for search result") {
		t.Fatalf("unrecognized timeout: %v", err)
	}
	if result.Answer != "" || result.ConversationID != "" || len(result.Sources) != 0 {
		t.Fatalf("incomplete conversation was returned as success: %#v", result)
	}
	if polls.Load() < 1 {
		t.Fatal("search poll never contacted upstream")
	}
}

func TestOpenAISearchPollingRateLimitDoesNotRetryProxy(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"detail":"slow down"}`)),
			Request:    request,
		}, nil
	})}
	manager := proxyruntime.NewManager("", nil)
	manager.ConfigureImageGroups("group:images", []proxyruntime.GroupConfig{{ID: "images", Enabled: true, Nodes: []proxyruntime.NodeConfig{
		{ID: "current", URL: "http://current.invalid:8080", Enabled: true},
		{ID: "stable", URL: "http://stable.invalid:8080", Enabled: true, RuntimeSuccesses: 3},
	}}})
	chat := NewOpenAIChat(NewOpenAIImage("https://example.invalid", client, manager, time.Second))
	_, err := chat.loadSearchConversation(proxyruntime.WithURL(context.Background(), "http://current.invalid:8080"), accounts.Account{}, "conversation-search")
	var upstream *protocol.UpstreamError
	if !errors.As(err, &upstream) || upstream.Status != http.StatusTooManyRequests || attempts.Load() != 1 {
		t.Fatalf("429 caused a duplicate proxy request: attempts=%d err=%v", attempts.Load(), err)
	}
}

func newSearchPollChat(t *testing.T, handler http.HandlerFunc, timeout, interval time.Duration) *OpenAIChat {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	image := NewOpenAIImage(server.URL, server.Client(), nil, timeout)
	image.pollInterval = interval
	return NewOpenAIChat(image)
}

func writeSearchPollConversation(w http.ResponseWriter, status, answer string, sources []any) {
	w.Header().Set("Content-Type", "application/json")
	metadata := map[string]any{}
	if openAISearchStatusDone(status) {
		metadata["finish_details"] = map[string]any{"type": status}
	} else if status != "" {
		metadata["status"] = status
	}
	if len(sources) > 0 {
		metadata["content_references"] = sources
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"mapping": map[string]any{
			"assistant": map[string]any{"message": map[string]any{
				"author":      map[string]any{"role": "assistant"},
				"create_time": 1.0,
				"metadata":    metadata,
				"content":     map[string]any{"content_type": "text", "parts": []any{answer}},
			}},
		},
	})
}
