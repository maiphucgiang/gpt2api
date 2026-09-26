package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/provider"
	"github.com/maiphucgiang/gpt2api/internal/store"
)

const imageTaskHistoryLimit = 2000

var (
	errImageTaskQueueFull = errors.New("image task queue is full")
	errImageTaskStorage   = errors.New("image task storage is unavailable")
	errImageTaskConflict  = errors.New("client_task_id already exists with different input")
	errImageTaskClosed    = errors.New("image task service is stopping")
)

type imageTaskInput struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Hash string `json:"hash"`
}

type imageTaskOutput struct {
	Status      string                   `json:"status"`
	AccountID   string                   `json:"account_id,omitempty"`
	UserID      string                   `json:"user_id,omitempty"`
	WorkspaceID string                   `json:"workspace_id,omitempty"`
	Checkpoint  provider.ImageCheckpoint `json:"checkpoint"`
	Data        map[string]string        `json:"data,omitempty"`
	Error       string                   `json:"error,omitempty"`
	ErrorCode   string                   `json:"error_code,omitempty"`
	Resumable   bool                     `json:"resumable,omitempty"`
	CallID      string                   `json:"-"`
}

type imageTaskState struct {
	ID          string            `json:"id"`
	OwnerID     string            `json:"owner_id"`
	Status      string            `json:"status"`
	Mode        string            `json:"mode"`
	Model       string            `json:"model"`
	N           int               `json:"n"`
	Size        string            `json:"size"`
	Quality     string            `json:"quality"`
	Fingerprint string            `json:"fingerprint"`
	PublicBase  string            `json:"public_base"`
	Prompt      string            `json:"-"`
	InputDir    string            `json:"input_dir,omitempty"`
	Inputs      []imageTaskInput  `json:"inputs,omitempty"`
	Outputs     []imageTaskOutput `json:"outputs"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

type imageTaskWork struct {
	Key     string
	Index   int
	Resume  bool
	Timeout time.Duration
}

type imageTaskAdmission struct {
	server *Server
	active bool
}

func imageTaskKey(owner, id string) string {
	raw, _ := json.Marshal([]string{owner, id})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func imageTaskFingerprint(task *imageTaskState) string {
	raw, _ := json.Marshal(struct {
		Mode, Model, Prompt, Size, Quality string
		N                                  int
		Inputs                             []imageTaskInput
	}{task.Mode, task.Model, task.Prompt, task.Size, task.Quality, task.N, task.Inputs})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Server) imageTasksPath() string { return filepath.Join(s.cfg.DataDir, "image_tasks.json") }
func (s *Server) imageTaskInputsPath() string {
	return filepath.Join(s.cfg.DataDir, "image_task_inputs")
}

func (s *Server) initImageTasks() {
	if s.cfg.ImageTaskWorkers <= 0 {
		s.cfg.ImageTaskWorkers = 2
	}
	if s.cfg.ImageTaskQueueSize <= 0 {
		s.cfg.ImageTaskQueueSize = 64
	}
	if s.cfg.ImageTaskRetention <= 0 {
		s.cfg.ImageTaskRetention = 24 * time.Hour
	}
	s.imageTaskQueue = make(chan imageTaskWork, s.cfg.ImageTaskWorkers+s.cfg.ImageTaskQueueSize)
	s.imageTaskPreparers = make(chan struct{}, min(s.cfg.ImageTaskWorkers, 4))
	if s.cfg.DataDir == "" {
		s.imageTaskLoadErr = errImageTaskStorage
		return
	}
	raw, err := os.ReadFile(s.imageTasksPath())
	if errors.Is(err, os.ErrNotExist) {
		s.cleanupImageTaskOrphans()
		return
	}
	if err != nil {
		s.imageTaskLoadErr = err
		return
	}
	var document struct {
		Version int               `json:"version"`
		Tasks   []*imageTaskState `json:"tasks"`
	}
	if json.Unmarshal(raw, &document) != nil || document.Version != 1 {
		s.imageTaskLoadErr = errImageTaskStorage
		return
	}
	for _, task := range document.Tasks {
		if task == nil || !validImageTaskID(task.ID) || task.OwnerID == "" || task.N < 1 || task.N > 10 || len(task.Outputs) != task.N {
			s.imageTaskLoadErr = errImageTaskStorage
			return
		}
		key := imageTaskKey(task.OwnerID, task.ID)
		if s.imageTasks[key] != nil {
			s.imageTaskLoadErr = errImageTaskStorage
			return
		}
		for i := range task.Outputs {
			output := &task.Outputs[i]
			switch output.Status {
			case "queued", "running":
				output.Status = "error"
				output.Error = "服务已重启；已提交的图片可继续查询，未确认提交的图片不会自动重新生成"
				output.ErrorCode = "task_interrupted"
				output.Resumable = output.AccountID != "" && output.Checkpoint.ConversationID != ""
			case "success", "error":
			default:
				s.imageTaskLoadErr = errImageTaskStorage
				return
			}
		}
		task.InputDir = ""
		refreshImageTaskState(task)
		s.imageTasks[key] = task
	}
	s.pruneImageTasksLocked(false)
	s.imageTaskWriteErr = s.persistImageTasksLocked()
	if s.imageTaskWriteErr == nil {
		s.cleanupImageTaskOrphans()
	}
}

func (s *Server) persistImageTasksLocked() error {
	tasks := make([]*imageTaskState, 0, len(s.imageTasks))
	for _, task := range s.imageTasks {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return imageTaskKey(tasks[i].OwnerID, tasks[i].ID) < imageTaskKey(tasks[j].OwnerID, tasks[j].ID)
	})
	err := store.WriteJSONFile(s.imageTasksPath(), struct {
		Version int               `json:"version"`
		Tasks   []*imageTaskState `json:"tasks"`
	}{1, tasks})
	s.imageTaskWriteErr = err
	return err
}

func (s *Server) pruneImageTasksLocked(makeRoom bool) {
	now := time.Now()
	type entry struct {
		key     string
		updated time.Time
	}
	terminal := []entry{}
	for key, task := range s.imageTasks {
		if task.Status == "running" || task.Status == "queued" {
			continue
		}
		updated, _ := time.Parse(time.RFC3339Nano, task.UpdatedAt)
		if now.Sub(updated) > s.cfg.ImageTaskRetention {
			delete(s.imageTasks, key)
			continue
		}
		terminal = append(terminal, entry{key, updated})
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].updated.Before(terminal[j].updated) })
	limit := imageTaskHistoryLimit
	if makeRoom {
		limit--
	}
	for _, entry := range terminal {
		if len(s.imageTasks) <= limit {
			break
		}
		delete(s.imageTasks, entry.key)
	}
}

func (s *Server) reserveImageTask() (*imageTaskAdmission, error) {
	s.imageTaskMu.Lock()
	defer s.imageTaskMu.Unlock()
	if s.imageTaskClosed {
		return nil, errImageTaskClosed
	}
	if s.imageTaskLoadErr != nil {
		return nil, errImageTaskStorage
	}
	if s.imageTaskWriteErr != nil && s.persistImageTasksLocked() != nil {
		return nil, errImageTaskStorage
	}
	if s.imageTaskPending+s.imageTaskReserved >= cap(s.imageTaskQueue) {
		return nil, errImageTaskQueueFull
	}
	select {
	case s.imageTaskPreparers <- struct{}{}:
	default:
		return nil, errImageTaskQueueFull
	}
	s.imageTaskReserved++
	return &imageTaskAdmission{server: s, active: true}, nil
}

func (a *imageTaskAdmission) release() {
	s := a.server
	s.imageTaskMu.Lock()
	defer s.imageTaskMu.Unlock()
	if a.active {
		a.active = false
		s.imageTaskReserved--
		<-s.imageTaskPreparers
	}
}

func (s *Server) submitImageTask(task *imageTaskState, admission *imageTaskAdmission) (map[string]any, error) {
	s.imageTaskMu.Lock()
	defer s.imageTaskMu.Unlock()
	if s.imageTaskClosed {
		return nil, errImageTaskClosed
	}
	key := imageTaskKey(task.OwnerID, task.ID)
	task.Fingerprint = imageTaskFingerprint(task)
	if previous := s.imageTasks[key]; previous != nil {
		if previous.Fingerprint != task.Fingerprint {
			return nil, errImageTaskConflict
		}
		return imageTaskPublic(previous), nil
	}
	s.pruneImageTasksLocked(true)
	if s.imageTaskPending+s.imageTaskReserved-1+task.N > cap(s.imageTaskQueue) || len(s.imageTasks) >= imageTaskHistoryLimit {
		return nil, errImageTaskQueueFull
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	task.CreatedAt, task.UpdatedAt, task.Status = now, now, "queued"
	task.Outputs = make([]imageTaskOutput, task.N)
	for i := range task.Outputs {
		task.Outputs[i].Status = "queued"
	}
	s.imageTasks[key] = task
	if err := s.persistImageTasksLocked(); err != nil {
		delete(s.imageTasks, key)
		return nil, errImageTaskStorage
	}
	admission.active = false
	s.imageTaskReserved--
	<-s.imageTaskPreparers
	for i := range task.Outputs {
		s.enqueueImageTaskLocked(task, key, i, false, 0)
	}
	s.startImageTaskWorkersLocked()
	return imageTaskPublic(task), nil
}

func (s *Server) enqueueImageTaskLocked(task *imageTaskState, key string, index int, resume bool, timeout time.Duration) {
	output := &task.Outputs[index]
	output.CallID = fmt.Sprintf("image-task-%s-%d-%d", key[:16], index, time.Now().UnixNano())
	s.monitor.start(output.CallID, "/api/image-tasks", task.Model, task.Mode)
	s.monitor.enrich(output.CallID, map[string]any{"key_id": task.OwnerID})
	s.monitor.update(output.CallID, "handler_queued", 0, "")
	s.imageTaskPending++
	s.imageTaskQueue <- imageTaskWork{key, index, resume, timeout}
}

func (s *Server) startImageTaskWorkersLocked() {
	if s.imageTaskWorkersStarted {
		return
	}
	s.imageTaskWorkersStarted = true
	s.imageTaskWorkers.Add(s.cfg.ImageTaskWorkers)
	for i := 0; i < s.cfg.ImageTaskWorkers; i++ {
		go s.imageTaskWorker()
	}
}

func refreshImageTaskState(task *imageTaskState) {
	queued, running, failed := false, false, false
	for _, output := range task.Outputs {
		queued = queued || output.Status == "queued"
		running = running || output.Status == "running"
		failed = failed || output.Status == "error"
	}
	switch {
	case running:
		task.Status = "running"
	case queued:
		task.Status = "queued"
	case failed:
		task.Status = "error"
	default:
		task.Status = "success"
	}
}

func imageTaskPublic(task *imageTaskState) map[string]any {
	value := map[string]any{"id": task.ID, "status": task.Status, "mode": task.Mode, "model": task.Model, "n": task.N, "size": task.Size, "quality": task.Quality, "created_at": task.CreatedAt, "updated_at": task.UpdatedAt, "can_resume_poll": false}
	data := []map[string]string{}
	completed := 0
	for _, output := range task.Outputs {
		if output.Status == "success" {
			completed++
			if output.Data != nil {
				data = append(data, copyStringMap(output.Data))
			}
		}
		if output.Error != "" && value["error"] == nil {
			value["error"], value["error_code"] = output.Error, output.ErrorCode
		}
		if task.Status == "error" && output.Status == "error" && output.Resumable && output.AccountID != "" && output.Checkpoint.ConversationID != "" {
			value["can_resume_poll"] = true
		}
	}
	value["progress"] = fmt.Sprintf("%d/%d", completed, task.N)
	if len(data) > 0 {
		value["data"] = data
	}
	return value
}

func copyStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (s *Server) cleanupImageTaskInputDir(name string) {
	if name == "" || !strings.HasPrefix(name, "upload-") || filepath.Base(name) != name {
		return
	}
	directory := filepath.Join(s.imageTaskInputsPath(), name)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() && len(entry.Name()) == 1 && entry.Name()[0] >= '0' && entry.Name()[0] <= '6' {
			_ = os.Remove(filepath.Join(directory, entry.Name()))
		}
	}
	_ = os.Remove(directory)
}

func (s *Server) cleanupImageTaskOrphans() {
	entries, _ := os.ReadDir(s.imageTaskInputsPath())
	for _, entry := range entries {
		if entry.IsDir() {
			s.cleanupImageTaskInputDir(entry.Name())
		}
	}
}

func (s *Server) closeImageTasks() error {
	s.imageTaskMu.Lock()
	s.imageTaskClosed = true
	s.imageTaskMu.Unlock()
	s.imageTaskWorkers.Wait()
	s.imageTaskMu.Lock()
	defer s.imageTaskMu.Unlock()
	changed := false
	for _, task := range s.imageTasks {
		for i := range task.Outputs {
			output := &task.Outputs[i]
			if output.Status == "queued" || output.Status == "running" {
				output.Status, output.ErrorCode, output.Error = "error", "task_interrupted", "服务已停止；任务不会自动重新生成"
				output.Resumable = output.AccountID != "" && output.Checkpoint.ConversationID != ""
				s.monitor.finish(output.CallID, "cancelled", task.Model, task.Mode, output.Error)
				task.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
				changed = true
			}
		}
		refreshImageTaskState(task)
		s.cleanupImageTaskInputDir(task.InputDir)
		task.InputDir = ""
	}
	s.imageTaskPending = 0
	if s.imageTaskLoadErr != nil {
		return s.imageTaskLoadErr
	}
	if changed || s.imageTaskWriteErr != nil {
		return s.persistImageTasksLocked()
	}
	return nil
}
