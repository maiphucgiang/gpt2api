package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/provider"
)

func markImageSubmissionUncertain(w http.ResponseWriter, err error) {
	if provider.ImageSubmissionUncertain(err) {
		w.Header().Set("X-Image-Submission-Uncertain", "true")
	}
}

func (s *Server) executeImageReservation(w http.ResponseWriter, r *http.Request, edit bool) {
	var request imageEditRequest
	callID := ""
	if edit {
		r.Body = http.MaxBytesReader(w, r.Body, 113<<20)
		parsed, err := s.parseImageEditRequest(r)
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		if err != nil {
			writeError(w, 400, err.Error(), "invalid_request_error")
			return
		}
		request = parsed
		callID = r.FormValue("_call_id")
		if request.HasMask || len(request.Inputs) == 0 {
			writeError(w, 400, "reference images are required and masks are unsupported", "invalid_request_error")
			return
		}
	} else {
		var body struct {
			Request struct {
				Prompt  string `json:"prompt"`
				Model   string `json:"model"`
				N       int    `json:"n"`
				Size    string `json:"size"`
				Quality string `json:"quality"`
				Format  string `json:"response_format"`
				CallID  string `json:"_call_id"`
			} `json:"request"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		value := body.Request
		request = imageEditRequest{Model: value.Model, Prompt: value.Prompt, N: value.N, Size: value.Size, Quality: value.Quality, ResponseFormat: value.Format}
		callID = value.CallID
	}
	parameters := &imageTaskState{ID: "internal", Model: request.Model, Prompt: request.Prompt, N: request.N, Size: request.Size, Quality: request.Quality}
	if err := normalizeImageTask(parameters); err != nil {
		writeError(w, 400, err.Error(), "invalid_request_error")
		return
	}
	format := firstNonEmpty(request.ResponseFormat, "url")
	if format != "url" && format != "b64_json" {
		writeError(w, 400, "invalid response_format", "invalid_request_error")
		return
	}
	if callID != "" {
		r = r.WithContext(context.WithValue(r.Context(), monitorCallIDKey{}, callID))
	}
	data, err := s.generateOpenAIImageData(r, r.Context(), parameters.Prompt, parameters.Model, parameters.Size, parameters.Quality, request.Inputs, format, requestPublicBase(r), parameters.N)
	if err != nil {
		markImageSubmissionUncertain(w, err)
		writeError(w, upstreamStatus(err), err.Error(), "upstream_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": time.Now().Unix(), "data": data})
}
