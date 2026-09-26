package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExecuteResponseRequiresImagesAndHonorsNoReplay(t *testing.T) {
	cases := []struct {
		name                        string
		status                      int
		body                        string
		uncertain, retry, wantError bool
	}{
		{"image", 200, `{"data":[{"url":"/images/fixture.png"}]}`, false, false, false},
		{"placeholder", 200, `{"ok":true,"execution":"placeholder"}`, false, false, true},
		{"accepted_timeout", 504, `{"error":"timeout"}`, true, false, true},
		{"rejected_before_submission", 429, `{"error":"quota"}`, false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.uncertain {
					w.Header().Set("X-Image-Submission-Uncertain", "true")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			gateway := &server{client: upstream.Client()}
			request, _ := http.NewRequest(http.MethodPost, upstream.URL, strings.NewReader(`{}`))
			_, status, err := gateway.executeRequest(request)
			if (err != nil) != tc.wantError || retryableTaskError(err, status) != tc.retry {
				t.Fatalf("execution: status=%d error=%v retry=%v", status, err, retryableTaskError(err, status))
			}
		})
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, io.ErrUnexpectedEOF
}

func TestExecuteTransportLossIsNotReplayed(t *testing.T) {
	gateway := &server{client: &http.Client{Transport: failingTransport{}}}
	request, _ := http.NewRequest(http.MethodPost, "http://fixture/execute", nil)
	_, status, err := gateway.executeRequest(request)
	if err == nil || retryableTaskError(err, status) {
		t.Fatal("ambiguous execute POST can be replayed")
	}
	if !retryableTaskError(errors.New("reservation unavailable"), 0) {
		t.Fatal("safe reservation retry was disabled")
	}
}
