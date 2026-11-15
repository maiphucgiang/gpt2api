package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/model"
	"github.com/maiphucgiang/gpt2api/internal/protocol"
)

func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	if !s.requireAPI(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed", "invalid_request_error")
		return
	}
	var raw json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return
	}
	var request protocol.ResponsesRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		writeError(w, 400, "invalid Responses request", "invalid_request_error")
		return
	}
	if strings.TrimSpace(request.Model) == "" {
		writeError(w, 400, "model is required", "invalid_request_error")
		return
	}
	route, ok := model.ResolveChat(request.Model)
	if !ok {
		writeError(w, 404, "model not found", "invalid_request_error")
		return
	}
	messages, err := protocol.ParseResponsesInput(request.Input, request.Instructions)
	if err == nil {
		err = validateResponsesRequest(request, route, messages)
	}
	if err != nil {
		writeError(w, 400, err.Error(), "invalid_request_error")
		return
	}
	chat := protocol.ChatRequest{Model: request.Model, Messages: messages, Temperature: request.Temperature, TopP: request.TopP, MaxTokens: request.MaxOutputTokens, Tools: request.Tools, ToolChoice: request.ToolChoice, ReasoningEffort: stringValue(request.Reasoning["effort"])}
	if request.Stream {
		s.streamWebResponses(w, r, chat, route)
	} else {
		s.completeWebResponses(w, r, chat, route)
	}
}

func (s *Server) completeWebResponses(w http.ResponseWriter, r *http.Request, request protocol.ChatRequest, route model.ChatRoute) {
	recorder := &responseCapture{header: make(http.Header)}
	s.completeChat(recorder, r, request, route)
	if recorder.status >= 400 {
		for key, values := range recorder.header {
			w.Header()[key] = values
		}
		w.WriteHeader(recorder.status)
		_, _ = w.Write(recorder.body.Bytes())
		return
	}
	var chat map[string]any
	if err := json.Unmarshal(recorder.body.Bytes(), &chat); err != nil {
		writeError(w, 502, "invalid internal response", "server_error")
		return
	}
	writeJSON(w, 200, responseFromChat(chat, request.Model))
}

func (s *Server) streamWebResponses(w http.ResponseWriter, r *http.Request, request protocol.ChatRequest, route model.ChatRoute) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	request.Stream = true
	stream := newResponsesStreamWriter(w, ctx, cancel, request)
	if stream.err != nil || ctx.Err() != nil {
		return
	}
	if route.Image {
		s.streamOpenAIImageChat(stream, r, request)
	} else {
		s.streamOpenAIChat(stream, r, request, route)
	}
	stream.finish()
}

func responseFromChat(chat map[string]any, modelName string) map[string]any {
	id := strings.TrimPrefix(stringValue(chat["id"]), "chatcmpl-")
	content := ""
	output := []any{}
	if choices, ok := chat["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if message, ok := choice["message"].(map[string]any); ok {
				if calls, ok := message["tool_calls"].([]any); ok {
					for _, raw := range calls {
						call, _ := raw.(map[string]any)
						fn, _ := call["function"].(map[string]any)
						output = append(output, map[string]any{"id": call["id"], "type": "function_call", "call_id": call["id"], "name": fn["name"], "arguments": fn["arguments"], "status": "completed"})
					}
				} else {
					content = stringValue(message["content"])
				}
			}
		}
	}
	if len(output) == 0 {
		output = []any{map[string]any{"id": "msg_" + id, "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": content, "annotations": []any{}}}}}
	}
	usage, _ := chat["usage"].(map[string]any)
	return map[string]any{"id": "resp_" + id, "object": "response", "created_at": time.Now().Unix(), "status": "completed", "model": modelName, "output": output, "usage": responsesUsage(usage), "store": false, "parallel_tool_calls": false, "tool_choice": "none", "tools": []any{}}
}

type responseCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *responseCapture) Header() http.Header    { return r.header }
func (r *responseCapture) WriteHeader(status int) { r.status = status }
func (r *responseCapture) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(data)
}
func writeEventSSE(w http.ResponseWriter, event string, value any) {
	raw, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
}
