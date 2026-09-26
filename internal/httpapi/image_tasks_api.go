package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/model"
	"github.com/maiphucgiang/gpt2api/internal/provider"
)

var imageTaskIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func validImageTaskID(id string) bool {
	return id != "." && id != ".." && imageTaskIDPattern.MatchString(id)
}

func normalizeImageTask(task *imageTaskState) error {
	task.ID, task.Prompt = strings.TrimSpace(task.ID), strings.TrimSpace(task.Prompt)
	if !validImageTaskID(task.ID) || task.Prompt == "" || len(task.Prompt) > 64<<10 {
		return errors.New("client_task_id must be a safe identifier and prompt must contain 1–65536 bytes")
	}
	task.Model = firstNonEmpty(task.Model, "gpt-image-2")
	spec, ok := model.Find(model.Catalog(), task.Model)
	if !ok || spec.Capability&model.Image == 0 || !isOpenAIImageModel(task.Model) {
		return errors.New("model is not an image model")
	}
	if task.N == 0 {
		task.N = 1
	}
	if task.N < 1 || task.N > 10 {
		return errors.New("n must be between 1 and 10")
	}
	task.Size = provider.NormalizeOpenAIImageSize(firstNonEmpty(task.Size, "1024x1024"))
	if !validOpenAIImageSize(task.Size) {
		return errors.New("invalid image size")
	}
	task.Quality = firstNonEmpty(task.Quality, "auto")
	switch task.Quality {
	case "auto", "low", "medium", "high":
	default:
		return errors.New("invalid image quality")
	}
	return nil
}

func writeImageTaskError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, errImageTaskConflict):
		status = http.StatusConflict
	case errors.Is(err, errImageTaskQueueFull):
		status = http.StatusTooManyRequests
		w.Header().Set("Retry-After", "2")
	}
	writeError(w, status, err.Error(), "image_task_error")
}

func (s *Server) imageTasksAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAPI(w, r) {
		return
	}
	if strings.HasSuffix(r.URL.Path, "/generations") && r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed", "invalid_request_error")
		return
	}
	if r.Method == http.MethodGet {
		owner := s.authIdentity(r)
		items, missing := []map[string]any{}, []string{}
		s.imageTaskMu.RLock()
		if s.imageTaskLoadErr != nil {
			s.imageTaskMu.RUnlock()
			writeImageTaskError(w, errImageTaskStorage)
			return
		}
		if ids := strings.TrimSpace(r.URL.Query().Get("ids")); ids != "" {
			for _, id := range strings.Split(ids, ",") {
				id = strings.TrimSpace(id)
				if id == "" {
					continue
				}
				if task := s.imageTasks[imageTaskKey(owner, id)]; task != nil {
					items = append(items, imageTaskPublic(task))
				} else {
					missing = append(missing, id)
				}
			}
		} else {
			for _, task := range s.imageTasks {
				if task.OwnerID == owner {
					items = append(items, imageTaskPublic(task))
				}
			}
			sort.Slice(items, func(i, j int) bool { return stringValue(items[i]["updated_at"]) > stringValue(items[j]["updated_at"]) })
		}
		s.imageTaskMu.RUnlock()
		writeJSON(w, 200, map[string]any{"items": items, "missing_ids": missing, "quota_summary": s.imageQuota()})
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed", "invalid_request_error")
		return
	}
	admission, err := s.reserveImageTask()
	if err != nil {
		writeImageTaskError(w, err)
		return
	}
	defer admission.release()
	var body struct {
		ID      string `json:"client_task_id"`
		Prompt  string `json:"prompt"`
		Model   string `json:"model"`
		N       int    `json:"n"`
		Size    string `json:"size"`
		Quality string `json:"quality"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	if !decodeJSON(w, r, &body) {
		return
	}
	task := &imageTaskState{ID: body.ID, OwnerID: s.authIdentity(r), Mode: "generate", Prompt: body.Prompt, Model: body.Model, N: body.N, Size: body.Size, Quality: body.Quality, PublicBase: requestPublicBase(r)}
	if err := normalizeImageTask(task); err != nil {
		writeError(w, 400, err.Error(), "invalid_request_error")
		return
	}
	value, err := s.submitImageTask(task, admission)
	if err != nil {
		writeImageTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}

func (s *Server) imageTaskEdits(w http.ResponseWriter, r *http.Request) {
	if !s.requireAPI(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed", "invalid_request_error")
		return
	}
	admission, err := s.reserveImageTask()
	if err != nil {
		writeImageTaskError(w, err)
		return
	}
	defer admission.release()
	r.Body = http.MaxBytesReader(w, r.Body, 113<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, 400, "invalid or oversized multipart form", "invalid_request_error")
		return
	}
	defer r.MultipartForm.RemoveAll()
	task := &imageTaskState{ID: r.FormValue("client_task_id"), OwnerID: s.authIdentity(r), Mode: "edit", Prompt: r.FormValue("prompt"), Model: r.FormValue("model"), Size: r.FormValue("size"), Quality: r.FormValue("quality"), PublicBase: requestPublicBase(r)}
	if n := strings.TrimSpace(r.FormValue("n")); n != "" {
		task.N, err = strconv.Atoi(n)
		if err != nil {
			writeError(w, 400, "invalid n", "invalid_request_error")
			return
		}
	}
	if err := normalizeImageTask(task); err != nil {
		writeError(w, 400, err.Error(), "invalid_request_error")
		return
	}
	if hasMultipartField(r.MultipartForm, imageEditMaskFields) {
		writeError(w, 400, "image masks are not supported", "invalid_request_error")
		return
	}
	count := 0
	for _, field := range imageEditReferenceFields {
		count += len(r.MultipartForm.File[field]) + len(r.MultipartForm.Value[field])
	}
	if count < 1 || count > 7 {
		writeError(w, 400, "between 1 and 7 reference images are required", "invalid_request_error")
		return
	}
	if err := os.MkdirAll(s.imageTaskInputsPath(), 0700); err != nil {
		writeImageTaskError(w, errImageTaskStorage)
		return
	}
	directory, err := os.MkdirTemp(s.imageTaskInputsPath(), "upload-")
	if err != nil {
		writeImageTaskError(w, errImageTaskStorage)
		return
	}
	task.InputDir = filepath.Base(directory)
	inputDir := task.InputDir
	defer func() {
		if admission.active {
			s.cleanupImageTaskInputDir(inputDir)
		}
	}()
	save := func(input provider.OpenAIImageInput) error {
		if len(input.Data) == 0 || len(input.Data) > 16<<20 {
			return errors.New("each reference image must contain 1–16777216 bytes")
		}
		path := filepath.Join(directory, fmt.Sprint(len(task.Inputs)))
		if err := os.WriteFile(path, input.Data, 0600); err != nil {
			return errImageTaskStorage
		}
		sum := sha256.Sum256(input.Data)
		task.Inputs = append(task.Inputs, imageTaskInput{Name: filepath.Base(input.Name), MIME: input.MIME, Hash: hex.EncodeToString(sum[:])})
		return nil
	}
	for _, field := range imageEditReferenceFields {
		for _, header := range r.MultipartForm.File[field] {
			if header.Size > 16<<20 {
				writeError(w, 413, "reference image exceeds 16MB", "invalid_request_error")
				return
			}
			input, err := imageInputFromFileHeader(header)
			if err == nil {
				err = save(input)
			}
			if err != nil {
				writeError(w, 400, err.Error(), "invalid_request_error")
				return
			}
		}
		for _, value := range r.MultipartForm.Value[field] {
			input, err := s.imageInputFromString(r.Context(), value)
			if err == nil {
				err = save(input)
			}
			if err != nil {
				writeError(w, 400, err.Error(), "invalid_request_error")
				return
			}
		}
	}
	value, err := s.submitImageTask(task, admission)
	if err != nil {
		writeImageTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}

func (s *Server) imageTaskByID(w http.ResponseWriter, r *http.Request) {
	if !s.requireAPI(w, r) {
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/image-tasks/"), "/")
	resume := strings.HasSuffix(path, "/resume-poll")
	if resume {
		path = strings.TrimSuffix(path, "/resume-poll")
	}
	if (!resume && r.Method != http.MethodGet) || (resume && r.Method != http.MethodPost) {
		writeError(w, 405, "method not allowed", "invalid_request_error")
		return
	}
	timeout := 30 * time.Second
	if resume && r.Body != nil && r.ContentLength != 0 {
		var body struct {
			Seconds int `json:"extra_timeout_secs"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		if !decodeJSON(w, r, &body) {
			return
		}
		if body.Seconds != 0 {
			if body.Seconds < 1 || body.Seconds > 120 {
				writeError(w, 400, "extra_timeout_secs must be between 1 and 120", "invalid_request_error")
				return
			}
			timeout = time.Duration(body.Seconds) * time.Second
		}
	}
	value, status, err := s.imageTaskAction(s.authIdentity(r), path, resume, timeout)
	if err != nil {
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "2")
		}
		writeError(w, status, err.Error(), "image_task_error")
		return
	}
	writeJSON(w, status, value)
}

func (s *Server) imageTaskAction(owner, id string, resume bool, timeout time.Duration) (map[string]any, int, error) {
	s.imageTaskMu.Lock()
	defer s.imageTaskMu.Unlock()
	if s.imageTaskLoadErr != nil {
		return nil, 503, errImageTaskStorage
	}
	key := imageTaskKey(owner, id)
	task := s.imageTasks[key]
	if task == nil {
		return nil, 404, errors.New("image task not found")
	}
	if !resume {
		return imageTaskPublic(task), 200, nil
	}
	if s.imageTaskClosed {
		return nil, 503, errImageTaskClosed
	}
	if task.Status == "queued" || task.Status == "running" {
		return imageTaskPublic(task), 202, nil
	}
	indexes := []int{}
	for index, output := range task.Outputs {
		if output.Status == "error" && output.Resumable && output.AccountID != "" && output.Checkpoint.ConversationID != "" {
			indexes = append(indexes, index)
		}
	}
	if len(indexes) == 0 {
		return nil, 409, errors.New("task has no resumable generation; it will not be resubmitted")
	}
	if s.imageTaskPending+s.imageTaskReserved+len(indexes) > cap(s.imageTaskQueue) {
		return nil, 429, errImageTaskQueueFull
	}
	previous := append([]imageTaskOutput(nil), task.Outputs...)
	oldTime := task.UpdatedAt
	for _, index := range indexes {
		task.Outputs[index].Status = "queued"
		task.Outputs[index].Error = ""
		task.Outputs[index].ErrorCode = ""
	}
	task.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	refreshImageTaskState(task)
	if err := s.persistImageTasksLocked(); err != nil {
		task.Outputs = previous
		task.UpdatedAt = oldTime
		refreshImageTaskState(task)
		return nil, 503, errImageTaskStorage
	}
	for _, index := range indexes {
		s.enqueueImageTaskLocked(task, key, index, true, timeout)
	}
	s.startImageTaskWorkersLocked()
	return imageTaskPublic(task), 202, nil
}
