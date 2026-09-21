package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (s *Server) registerAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/register")
	switch {
	case (path == "" || path == "/") && r.Method == http.MethodGet:
		s.registerConfig(w)
	case (path == "" || path == "/") && r.Method == http.MethodPost:
		s.updateRegisterConfig(w, r)
	case path == "/start" && r.Method == http.MethodPost:
		s.setRegisterEnabled(w, true)
	case path == "/stop" && r.Method == http.MethodPost:
		s.setRegisterEnabled(w, false)
	case path == "/reset" && r.Method == http.MethodPost:
		s.resetRegister(w)
	case path == "/runtime" && r.Method == http.MethodGet:
		s.registerRuntimeStatus(w)
	case path == "/checkout-retries/stop" && r.Method == http.MethodPost:
		s.stopCheckoutRetries(w)
	case path == "/checkout-history/clear" && r.Method == http.MethodPost:
		s.clearCheckoutHistory(w)
	case path == "/outlook-pool/reset" && r.Method == http.MethodPost:
		s.resetOutlookPool(w, r)
	case path == "/outlook-pool/retry-selected" && r.Method == http.MethodPost:
		s.retryOutlookPool(w, r)
	case path == "/gptmail/status" && r.Method == http.MethodPost:
		s.gptMailStatus(w, r, false)
	case path == "/gptmail/refresh-key" && r.Method == http.MethodPost:
		s.gptMailStatus(w, r, true)
	case path == "/openai/survival" && r.Method == http.MethodGet:
		s.openAISurvival(w)
	case path == "/openai/survival" && r.Method == http.MethodPost:
		s.updateOpenAISurvival(w, r)
	case path == "/openai/survival/run" && r.Method == http.MethodPost:
		s.runOpenAISurvival(w)
	case path == "/events" && r.Method == http.MethodGet:
		s.registerEvents(w, r)
	default:
		writeError(w, http.StatusNotFound, "register endpoint not found", "not_found")
	}
}

func (s *Server) registerConfig(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"register": s.registerStore.Get()})
}

func (s *Server) updateRegisterConfig(w http.ResponseWriter, r *http.Request) {
	var updates map[string]any
	if !decodeJSON(w, r, &updates) {
		return
	}
	value, err := s.registerStore.Update(updates)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"register": value})
}

func (s *Server) setRegisterEnabled(w http.ResponseWriter, enabled bool) {
	if enabled {
		config := s.registerStore.Get()
		target := stringValue(config["target"])
		if target == "" {
			target = "openai"
		}
		if err := s.registerRuntime.Start(target); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"ok":       false,
				"runtime":  s.registerRuntime.Status(target),
				"register": config,
			})
			return
		}
	} else {
		s.registerRuntime.Stop()
	}
	value, err := s.registerStore.SetEnabled(enabled)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"register": value})
}

func (s *Server) resetRegister(w http.ResponseWriter) {
	value, err := s.registerStore.Reset()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"register": value})
}

func (s *Server) registerAction(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{"ok": false, "runtime": "go", "error": message})
}

func (s *Server) resetOutlookPool(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scope string `json:"scope"`
	}
	if r.Body != nil && r.ContentLength != 0 && !decodeJSON(w, r, &body) {
		return
	}
	scope := strings.ToLower(strings.TrimSpace(body.Scope))
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "unused" && scope != "failed" && scope != "retryable" && scope != "invalid" {
		writeError(w, 400, "invalid Outlook pool reset scope", "invalid_request_error")
		return
	}
	value, err := s.registerStore.ResetOutlookPoolState(scope)
	if err != nil {
		writeError(w, 500, err.Error(), "server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"register": value, "runtime_available": false, "runtime_error": "Outlook token/Graph registration worker is not configured in Go runtime"})
}

func (s *Server) retryOutlookPool(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProviderID string   `json:"provider_id"`
		MailboxIDs []string `json:"mailbox_ids"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ProviderID) == "" || len(uniqueStrings(body.MailboxIDs)) == 0 {
		writeError(w, 400, "provider_id and mailbox_ids are required", "invalid_request_error")
		return
	}
	value, err := s.registerStore.QueueOutlookRetry(body.ProviderID, uniqueStrings(body.MailboxIDs))
	if err != nil {
		writeError(w, 500, err.Error(), "server_error")
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "register": value, "runtime_available": false, "runtime_error": "Outlook token/Graph registration worker is not configured in Go runtime"})
}

func (s *Server) gptMailStatus(w http.ResponseWriter, r *http.Request, refreshKey bool) {
	var body struct {
		Provider map[string]any `json:"provider"`
		Force    *bool          `json:"force"`
	}
	if r.Body != nil && r.ContentLength != 0 && !decodeJSON(w, r, &body) {
		return
	}
	config := s.registerStore.Get()
	mail := mapValue(config["mail"])
	providerConfig := body.Provider
	if providerConfig == nil {
		providerConfig = firstGPTMailConfig(mail)
	}
	if providerConfig == nil {
		writeError(w, http.StatusBadRequest, "未找到 GPTMail 邮箱来源", "invalid_request_error")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var result map[string]any
	var err error
	if refreshKey {
		force := body.Force == nil || *body.Force
		result, err = s.gptMail.RefreshPublicKey(ctx, providerConfig, force)
	} else {
		force := body.Force != nil && *body.Force
		result, err = s.gptMail.Status(ctx, providerConfig, force)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "upstream_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": result})
}

func firstGPTMailConfig(mail map[string]any) map[string]any {
	providers, _ := mail["providers"].([]any)
	for _, raw := range providers {
		item, ok := raw.(map[string]any)
		if ok && strings.EqualFold(stringValue(item["type"]), "gptmail") {
			return cloneMap(item)
		}
	}
	return nil
}

func (s *Server) stopCheckoutRetries(w http.ResponseWriter) {
	value, err := s.registerStore.StopCheckoutRetries()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"register": value})
}

func (s *Server) clearCheckoutHistory(w http.ResponseWriter) {
	value, err := s.registerStore.ClearCheckoutHistory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) registerRuntimeStatus(w http.ResponseWriter) {
	config := s.registerStore.Get()
	target := stringValue(config["target"])
	if target == "" {
		target = "openai"
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtime": s.registerRuntime.Status(target)})
}

func (s *Server) openAISurvival(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"survival": s.survivalSnapshot()})
}

func (s *Server) updateOpenAISurvival(w http.ResponseWriter, r *http.Request) {
	var updates map[string]any
	if !decodeJSON(w, r, &updates) {
		return
	}
	value, err := s.registerStore.Update(map[string]any{"openai_survival": updates})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	select {
	case s.survivalWake <- struct{}{}:
	default:
	}
	writeJSON(w, http.StatusOK, map[string]any{"survival": value["openai_survival"]})
}

func (s *Server) registerEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := ""
	for {
		payload := map[string]any{"register": s.registerStore.Get()}
		raw, _ := json.Marshal(payload)
		if string(raw) != last {
			writeSSE(w, payload)
			last = string(raw)
			if flusher != nil {
				flusher.Flush()
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
