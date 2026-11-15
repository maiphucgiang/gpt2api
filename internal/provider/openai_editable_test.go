package provider

import (
	"context"
	"encoding/base64"
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

func TestDecodeEditableInputsRejectsEmptyAndNonImage(t *testing.T) {
	if _, err := DecodeEditableInputs(nil); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty: %v", err)
	}
	png := onePixelPNG(t)
	tooMany := make([]string, EditableReferenceImageMaxCount+1)
	for i := range tooMany {
		tooMany[i] = base64.StdEncoding.EncodeToString(png)
	}
	if _, err := DecodeEditableInputs(tooMany); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("count: %v", err)
	}
	if _, err := DecodeEditableInputs([]string{base64.StdEncoding.EncodeToString([]byte("%PDF-1.4"))}); err == nil || !strings.Contains(err.Error(), "not an image") {
		t.Fatalf("mime: %v", err)
	}
}

func TestExportEditableUsesConfiguredModelAndEffort(t *testing.T) {
	var prepare, conversation map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveEditableUpstream(t, w, r, editableUpstreamState{
			onPrepare: func(payload map[string]any) { prepare = payload },
			onStart:   func(payload map[string]any) { conversation = payload },
		})
	}))
	t.Cleanup(server.Close)
	client := NewOpenAIImage(server.URL, server.Client(), nil, 5*time.Second)
	client.EditableModel = "gpt-5-test"
	client.EditableReasoningEffort = "high"
	client.pollInterval = time.Millisecond
	_, err := client.ExportEditable(context.Background(), accounts.Account{Token: "token"}, "ppt", "做一份说明", []OpenAIImageInput{{Name: "ref.png", MIME: "image/png", Data: onePixelPNG(t)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(prepare["model"]) != "gpt-5-test" || fmt.Sprint(prepare["thinking_effort"]) != "high" {
		t.Fatalf("prepare payload: %#v", prepare)
	}
	if fmt.Sprint(conversation["model"]) != "gpt-5-test" || fmt.Sprint(conversation["thinking_effort"]) != "high" {
		t.Fatalf("conversation payload: %#v", conversation)
	}
}

func TestPollEditableArtifactsHonorsRetryAfterAndDoesNotSwitchProxy(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts.Add(1)
		header := make(http.Header)
		header.Set("Retry-After", "1")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"detail":"slow down"}`)),
			Request:    request,
		}, nil
	})}
	manager := proxyruntime.NewManager("", nil)
	manager.ConfigureImageGroups("group:images", []proxyruntime.GroupConfig{{ID: "images", Enabled: true, Nodes: []proxyruntime.NodeConfig{
		{ID: "current", URL: "http://current.invalid:8080", Enabled: true},
		{ID: "stable", URL: "http://stable.invalid:8080", Enabled: true, RuntimeSuccesses: 3},
	}}})
	imageClient := NewOpenAIImage("https://example.invalid", client, manager, time.Second)
	imageClient.pollInterval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithOpenAIImageStage(ctx, func(metric string, _ time.Duration) {
		if metric == "retry_wait_ms" {
			cancel()
		}
	})
	_, err := imageClient.pollEditableArtifacts(proxyruntime.WithURL(ctx, "http://current.invalid:8080"), accounts.Account{}, "conversation-rate-limit", "ppt")
	var upstream *protocol.UpstreamError
	if !errors.As(err, &upstream) || upstream.Status != http.StatusTooManyRequests || attempts.Load() != 1 {
		t.Fatalf("429 caused a duplicate proxy request: attempts=%d err=%v", attempts.Load(), err)
	}
}

func TestPollEditableArtifactsStopsOnTerminalStatusAndBadJSON(t *testing.T) {
	t.Run("terminal", func(t *testing.T) {
		var polls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/backend-api/conversation/") {
				http.NotFound(w, r)
				return
			}
			polls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"mapping": map[string]any{"msg": map[string]any{"message": map[string]any{"status": "moderated"}}}})
		}))
		t.Cleanup(server.Close)
		client := NewOpenAIImage(server.URL, server.Client(), nil, time.Second)
		client.pollInterval = time.Millisecond
		_, err := client.pollEditableArtifacts(context.Background(), accounts.Account{Token: "token"}, "conversation-terminal", "ppt")
		if err == nil || !strings.Contains(err.Error(), "moderated") {
			t.Fatalf("terminal: %v", err)
		}
		if polls.Load() != 1 {
			t.Fatalf("continued polling after terminal status: %d", polls.Load())
		}
	})
	t.Run("bad json", func(t *testing.T) {
		var polls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/backend-api/conversation/") {
				http.NotFound(w, r)
				return
			}
			polls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "not-json")
		}))
		t.Cleanup(server.Close)
		client := NewOpenAIImage(server.URL, server.Client(), nil, time.Second)
		client.pollInterval = time.Millisecond
		_, err := client.pollEditableArtifacts(context.Background(), accounts.Account{Token: "token"}, "conversation-bad-json", "ppt")
		if err == nil || !isOpenAIJSONDecodeError(err) {
			t.Fatalf("bad json: %v", err)
		}
		if polls.Load() != 1 {
			t.Fatalf("continued polling after bad JSON: %d", polls.Load())
		}
	})
}

func TestResumeEditableDoesNotPostConversation(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/backend-api/f/conversation" {
			posts.Add(1)
		}
		serveEditableUpstream(t, w, r, editableUpstreamState{})
	}))
	t.Cleanup(server.Close)
	client := NewOpenAIImage(server.URL, server.Client(), nil, 5*time.Second)
	client.pollInterval = time.Millisecond
	result, err := client.ResumeEditable(context.Background(), accounts.Account{Token: "token"}, "conversation-editable", "ppt")
	if err != nil {
		t.Fatal(err)
	}
	if result.ConversationID != "conversation-editable" || posts.Load() != 0 {
		t.Fatalf("resume regenerated: %#v posts=%d", result, posts.Load())
	}
}

type editableUpstreamState struct {
	onPrepare func(map[string]any)
	onStart   func(map[string]any)
}

func serveEditableUpstream(t *testing.T, w http.ResponseWriter, r *http.Request, state editableUpstreamState) {
	t.Helper()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/":
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html data-build="test-build"><script src="/static/app.js"></script></html>`)
	case r.URL.Path == "/backend-api/sentinel/chat-requirements/prepare":
		_ = json.NewEncoder(w).Encode(map[string]any{"prepare_token": "prepare-token"})
	case r.URL.Path == "/backend-api/sentinel/chat-requirements/finalize":
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "requirements-token"})
	case r.Method == http.MethodPost && r.URL.Path == "/backend-api/files":
		_ = json.NewEncoder(w).Encode(map[string]any{"file_id": "file_reference", "upload_url": "http://" + r.Host + "/reference-upload"})
	case r.URL.Path == "/reference-upload":
		w.WriteHeader(http.StatusCreated)
	case strings.HasSuffix(r.URL.Path, "/uploaded"):
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/backend-api/f/conversation/prepare":
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if state.onPrepare != nil {
			state.onPrepare(payload)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"conduit_token": "conduit-token"})
	case r.URL.Path == "/backend-api/f/conversation":
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if state.onStart != nil {
			state.onStart(payload)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"conversation_id\":\"conversation-editable\"}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	case strings.Contains(r.URL.Path, "/attachment/") || strings.Contains(r.URL.Path, "/interpreter/"):
		http.NotFound(w, r)
	case strings.HasPrefix(r.URL.Path, "/backend-api/conversation/"):
		_ = json.NewEncoder(w).Encode(map[string]any{"mapping": map[string]any{
			"msg": map[string]any{"message": map[string]any{
				"id": "msg", "author": map[string]any{"role": "assistant"},
				"content": map[string]any{"parts": []any{
					map[string]any{"name": "deck.pptx", "file_id": "file_ppt", "mime_type": "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
					map[string]any{"name": "assets.zip", "file_id": "file_zip", "mime_type": "application/zip"},
				}},
			}},
		}})
	case strings.HasPrefix(r.URL.Path, "/backend-api/files/download/file_ppt"):
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
		_, _ = io.WriteString(w, "PPTX")
	case strings.HasPrefix(r.URL.Path, "/backend-api/files/download/file_zip"):
		w.Header().Set("Content-Type", "application/zip")
		_, _ = io.WriteString(w, "ZIP")
	default:
		http.NotFound(w, r)
	}
}

func TestEditablePollTimeoutKeepsTwentyMinuteFloor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	got := editablePollTimeout(180*time.Second, ctx)
	if got != 20*time.Minute {
		t.Fatalf("180s request timeout shortened poll to %v", got)
	}
	got = editablePollTimeout(180*time.Second, context.Background())
	if got != 20*time.Minute {
		t.Fatalf("no deadline: %v", got)
	}
	short, stop := context.WithTimeout(context.Background(), 10*time.Minute)
	defer stop()
	got = editablePollTimeout(180*time.Second, short)
	if got < 9*time.Minute || got > 10*time.Minute {
		t.Fatalf("context cap: %v", got)
	}
}
