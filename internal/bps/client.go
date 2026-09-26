package bps

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
	proxyruntime "github.com/maiphucgiang/gpt2api/internal/proxy"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
	Proxy   *proxyruntime.Manager
	Replay  *Replay
}

func NewClient(baseURL string, timeout time.Duration, transport http.RoundTripper, proxy *proxyruntime.Manager, replay *Replay) *Client {
	if baseURL == "" {
		baseURL = "https://bps.openai.com"
	}
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Proxy: proxy, Replay: replay}
}

func (c *Client) Run(ctx context.Context, account accounts.Account, p *Prepared, emit func(Object) error) (result Object, returned error) {
	id, expiry, err := Credentials(account)
	if err != nil {
		return nil, err
	}
	if !expiry.IsZero() && !expiry.After(time.Now()) {
		return nil, fail(401, "access_token_expired", "ChatGPT access token expired")
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "https" && !(base.Scheme == "http" && (base.Hostname() == "127.0.0.1" || base.Hostname() == "localhost" || base.Hostname() == "::1"))) {
		return nil, fail(500, "invalid_bps_url", "BPS upstream must use HTTPS (HTTP is allowed only for local tests)")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if c.Proxy != nil {
		ctx = proxyruntime.WithURL(ctx, c.Proxy.Resolve(account.Fields, false))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/basispoints/api/responses", bytes.NewReader(encode(p.Body)))
	if err != nil {
		return nil, fail(500, "invalid_bps_request", "cannot build BPS request")
	}
	req.Header.Set("Authorization", "Bearer "+account.Token)
	req.Header.Set("chatgpt-account-id", id)
	req.Header.Set("x-openai-account-id", id)
	req.Header.Set("x-basispoints-auth-mode", "chatgpt")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Origin", "https://bps.openai.com")
	req.Header.Set("x-openai-internal-basispoints-client-product", "basispoints-excel-plugin")
	req.Header.Set("x-openai-internal-basispoints-client-agent-profile", "excel")
	defer func() {
		var upstream *Error
		if errors.As(returned, &upstream) {
			copy := *upstream
			copy.Attempted = true
			returned = &copy
		}
	}()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fail(502, "bps_transport_error", "BPS request transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		code := "bps_upstream_error"
		if resp.StatusCode == 403 {
			code = "bps_access_denied"
		}
		return nil, fail(resp.StatusCode, code, fmt.Sprintf("BPS returned HTTP %d", resp.StatusCode))
	}
	defer func() {
		if returned != nil {
			var e *Error
			if errors.As(returned, &e) {
				copy := *e
				copy.Accepted = true
				returned = &copy
			}
		}
	}()
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return nil, fail(502, "invalid_bps_stream", "BPS did not return an SSE stream")
	}
	type part struct {
		event Object
		err   error
	}
	channel := make(chan part, 1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		send := func(v part) error {
			select {
			case channel <- v:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		err := decodeSSE(resp.Body, func(event Object) error { return send(part{event: event}) })
		_ = send(part{err: err})
	}()
	defer func() { cancel(); _ = resp.Body.Close(); <-stopped }()
	state := &streamState{p: p, replay: c.Replay, emit: emit, text: map[string]string{}, done: map[string]bool{}}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if state.base != nil {
				if err := state.send(Object{"type": "response.in_progress", "response": state.progress()}); err != nil {
					return nil, err
				}
			}
		case v := <-channel:
			if v.event == nil {
				if v.err != nil {
					return nil, v.err
				}
				return nil, fail(502, "bps_stream_incomplete", "BPS stream ended without a terminal response")
			}
			response, terminal, err := state.event(v.event)
			if err != nil {
				return nil, err
			}
			if terminal {
				return response, nil
			}
		}
	}
}

func decodeSSE(reader io.Reader, emit func(Object) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	eventName := ""
	lines := []string{}
	total, frame := 0, 0
	flush := func() error {
		defer func() { eventName = ""; lines = nil; frame = 0 }()
		if len(lines) == 0 {
			return nil
		}
		data := strings.Join(lines, "\n")
		if data == "[DONE]" {
			return nil
		}
		var value Object
		if Decode([]byte(data), &value) != nil || value == nil {
			return fail(502, "invalid_bps_event", "BPS emitted invalid JSON")
		}
		kind := String(value["type"])
		if kind == "" {
			kind = eventName
			value["type"] = kind
		}
		if kind == "" || (eventName != "" && eventName != kind) {
			return fail(502, "invalid_bps_event", "BPS event name does not match its payload")
		}
		return emit(value)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		total += len(line) + 1
		frame += len(line) + 1
		if total > 16<<20 || frame > 2<<20 {
			return fail(502, "bps_response_too_large", "BPS response exceeded size limits")
		}
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if scanner.Err() != nil {
		return fail(502, "bps_stream_interrupted", "BPS stream was interrupted")
	}
	return nil
}
