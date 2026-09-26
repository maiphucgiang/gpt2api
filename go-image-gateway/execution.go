package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// An execute POST cannot be replayed when its delivery or accepted result is unknown.
type uncertainExecutionError struct{ err error }

func (e *uncertainExecutionError) Error() string { return e.err.Error() }
func (e *uncertainExecutionError) Unwrap() error { return e.err }

func retryableTaskError(err error, status int) bool {
	var uncertain *uncertainExecutionError
	return err != nil && !errors.Is(err, context.Canceled) && !errors.As(err, &uncertain) && (status == 0 || status == http.StatusTooManyRequests || status >= 500)
}

func (s *server) executeRequest(request *http.Request) (json.RawMessage, int, error) {
	response, err := s.client.Do(request)
	if err != nil {
		return nil, 0, &uncertainExecutionError{err}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
	if err != nil {
		return nil, response.StatusCode, &uncertainExecutionError{err}
	}
	if len(raw) > 32<<20 {
		return nil, response.StatusCode, &uncertainExecutionError{errors.New("image response exceeds 32MB")}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		err = fmt.Errorf("scheduler execute HTTP %d", response.StatusCode)
		if response.Header.Get("X-Image-Submission-Uncertain") == "true" {
			err = &uncertainExecutionError{err}
		}
		return nil, response.StatusCode, err
	}
	var body struct {
		Data []struct {
			URL    string `json:"url"`
			Base64 string `json:"b64_json"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &body) != nil || len(body.Data) == 0 {
		return nil, response.StatusCode, &uncertainExecutionError{errors.New("scheduler returned no image result")}
	}
	for _, item := range body.Data {
		if item.URL == "" && item.Base64 == "" {
			return nil, response.StatusCode, &uncertainExecutionError{errors.New("scheduler returned an empty image")}
		}
	}
	return raw, response.StatusCode, nil
}
