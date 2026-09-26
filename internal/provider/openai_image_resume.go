package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
	"github.com/maiphucgiang/gpt2api/internal/protocol"
	proxyruntime "github.com/maiphucgiang/gpt2api/internal/proxy"
)

// ImageCheckpoint contains only the identifiers needed to retrieve an existing generation.
type ImageCheckpoint struct {
	ConversationID string   `json:"conversation_id"`
	InputFileIDs   []string `json:"input_file_ids,omitempty"`
	InputHashes    []string `json:"input_hashes,omitempty"`
}

type ImageExecutionError struct {
	Stage      string
	Checkpoint ImageCheckpoint
	Err        error
}

func (e *ImageExecutionError) Error() string { return e.Err.Error() }
func (e *ImageExecutionError) Unwrap() error { return e.Err }

// ImageSubmissionUncertain prevents automatic regeneration after an accepted or ambiguous POST.
func ImageSubmissionUncertain(err error) bool {
	var execution *ImageExecutionError
	if !errors.As(err, &execution) {
		return false
	}
	if execution.Stage == "submit" && execution.Checkpoint.ConversationID == "" {
		var upstream *protocol.UpstreamError
		if errors.As(err, &upstream) && (upstream.Status == http.StatusUnauthorized || upstream.Status == http.StatusForbidden || upstream.Status == http.StatusTooManyRequests) {
			return false
		}
	}
	return true
}

type imageCheckpointContextKey struct{}
type imageCheckpointFunc func(accounts.Account, ImageCheckpoint) error

func WithImageCheckpoint(ctx context.Context, fn func(accounts.Account, ImageCheckpoint) error) context.Context {
	return context.WithValue(ctx, imageCheckpointContextKey{}, imageCheckpointFunc(fn))
}

func notifyImageCheckpoint(ctx context.Context, account accounts.Account, state ImageCheckpoint) error {
	if fn, ok := ctx.Value(imageCheckpointContextKey{}).(imageCheckpointFunc); ok && fn != nil {
		return fn(account, state)
	}
	return nil
}

func newImageCheckpoint(inputs []OpenAIImageInput, references []openAIImageReference) ImageCheckpoint {
	state := ImageCheckpoint{}
	for _, ref := range references {
		if ref.FileID != "" {
			state.InputFileIDs = append(state.InputFileIDs, ref.FileID)
		}
	}
	for _, input := range inputs {
		if len(input.Data) > 0 {
			sum := sha256.Sum256(input.Data)
			state.InputHashes = append(state.InputHashes, hex.EncodeToString(sum[:]))
		}
	}
	return state
}

func (o *OpenAIImage) acquireImageEgress(ctx context.Context, account accounts.Account) (context.Context, func(error), error) {
	if o.Proxy == nil {
		return ctx, func(error) {}, nil
	}
	started := time.Now()
	lease, err := o.Proxy.AcquireImageContext(ctx, account.Fields)
	notifyOpenAIImageStage(ctx, "egress_acquire_ms", started)
	if err != nil {
		return ctx, nil, fmt.Errorf("wait for image proxy capacity: %w", err)
	}
	if lease.Source == "unavailable" {
		lease.Release(false)
		return ctx, nil, fmt.Errorf("no browser-compatible image proxy is available; use http, https, socks5, or socks5h")
	}
	ctx = proxyruntime.WithImageLease(proxyruntime.WithURL(ctx, lease.URL), lease)
	notifyOpenAIImageEgress(ctx, lease.URL)
	return ctx, func(err error) { lease.Release(openAIImageProxyFailure(err)) }, nil
}

// Resume retrieves an existing conversation without uploading inputs or submitting a generation.
func (o *OpenAIImage) Resume(ctx context.Context, account accounts.Account, state ImageCheckpoint) (results []ImageResult, err error) {
	if account.Token == "" || strings.TrimSpace(state.ConversationID) == "" {
		return nil, errors.New("account and conversation are required")
	}
	ctx, release, err := o.acquireImageEgress(ctx, account)
	if err != nil {
		return nil, err
	}
	defer func() { release(err) }()
	refs, err := o.pollConversation(ctx, account, state.ConversationID)
	if err != nil {
		return nil, &ImageExecutionError{Stage: "poll", Checkpoint: state, Err: err}
	}
	results, err = o.downloadImageResults(ctx, account, state, refs)
	if err != nil {
		return nil, &ImageExecutionError{Stage: "download", Checkpoint: state, Err: err}
	}
	return results, nil
}

func (o *OpenAIImage) downloadImageResults(ctx context.Context, account accounts.Account, state ImageCheckpoint, refs []string) ([]ImageResult, error) {
	results := make([]ImageResult, 0, len(refs))
	seen, inputs, hashes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, id := range state.InputFileIDs {
		inputs[id] = true
	}
	for _, hash := range state.InputHashes {
		hashes[hash] = true
	}
	var lastErr error
	// Newest generated assets precede echoed reference assets in the output selection.
	for index := len(refs) - 1; index >= 0; index-- {
		ref := refs[index]
		if ref == "" || seen[ref] || inputs[strings.TrimPrefix(ref, "file-service://")] {
			continue
		}
		seen[ref] = true
		started := time.Now()
		raw, mime, err := o.downloadImageRefWithRetry(ctx, account, state.ConversationID, ref)
		if err != nil {
			lastErr = err
			continue
		}
		sum := sha256.Sum256(raw)
		if hashes[hex.EncodeToString(sum[:])] {
			continue
		}
		notifyOpenAIImageStage(ctx, "download_ms", started)
		proxyruntime.ObserveImageLeaseStage(ctx, time.Since(started), openAIImageSlowDownload)
		results = append(results, ImageResult{Base64: base64.StdEncoding.EncodeToString(raw), MIME: mime})
	}
	if len(results) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("OpenAI image generation returned no downloadable files")
	}
	return results, nil
}
