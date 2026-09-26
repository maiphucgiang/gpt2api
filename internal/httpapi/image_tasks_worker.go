package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
	"github.com/maiphucgiang/gpt2api/internal/provider"
	"github.com/maiphucgiang/gpt2api/internal/store"
)

func (s *Server) imageTaskWorker() {
	defer s.imageTaskWorkers.Done()
	for {
		select {
		case <-s.backgroundContext.Done():
			return
		case work := <-s.imageTaskQueue:
			if s.backgroundContext.Err() != nil {
				return
			}
			s.executeImageTaskWork(work)
		}
	}
}

func (s *Server) executeImageTaskWork(work imageTaskWork) {
	s.imageTaskMu.Lock()
	task := s.imageTasks[work.Key]
	output := &task.Outputs[work.Index]
	output.Status = "running"
	task.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	refreshImageTaskState(task)
	persistErr := s.persistImageTasksLocked()
	snapshot, attempt := *task, *output
	s.imageTaskMu.Unlock()
	var data map[string]string
	var err error
	if persistErr != nil {
		err = errImageTaskStorage
	} else {
		timeout := imageRequestTotalTimeout(s.cfg.RequestTimeout)
		if work.Resume {
			timeout = work.Timeout
		}
		ctx, cancel := context.WithTimeout(s.backgroundContext, timeout)
		ctx = context.WithValue(ctx, monitorCallIDKey{}, attempt.CallID)
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://internal/api/image-tasks", nil)
		s.stageRequestMonitor(request, "handler_queue_done", 10, nil)
		if work.Resume {
			data, err = s.resumeImageTaskOutput(request, ctx, snapshot, attempt)
		} else {
			var inputs []provider.OpenAIImageInput
			inputs, err = s.loadImageTaskInputs(snapshot)
			if err == nil {
				ctx = provider.WithImageCheckpoint(ctx, func(account accounts.Account, checkpoint provider.ImageCheckpoint) error {
					s.imageTaskMu.Lock()
					defer s.imageTaskMu.Unlock()
					current := &s.imageTasks[work.Key].Outputs[work.Index]
					current.AccountID = store.AccountID(account.Fields)
					current.UserID = stringValue(account.Fields["user_id"])
					current.WorkspaceID = stringValue(account.Fields["account_id"])
					current.Checkpoint = checkpoint
					return s.persistImageTasksLocked()
				})
				var result []map[string]string
				result, err = s.generateOpenAIImageData(request, ctx, snapshot.Prompt, snapshot.Model, snapshot.Size, snapshot.Quality, inputs, "url", snapshot.PublicBase, 1)
				if err == nil && len(result) > 0 {
					data = result[0]
				}
			}
		}
		cancel()
	}
	s.imageTaskMu.Lock()
	defer s.imageTaskMu.Unlock()
	task = s.imageTasks[work.Key]
	output = &task.Outputs[work.Index]
	output.Error, output.ErrorCode, output.Resumable = "", "", false
	if err == nil && data != nil {
		output.Status, output.Data = "success", data
		s.monitor.finish(output.CallID, "success", task.Model, task.Mode, "")
	} else {
		output.Status = "error"
		output.Error, output.ErrorCode = "图片任务失败，请检查账号与上游状态", "image_task_failed"
		output.Resumable = output.AccountID != "" && output.Checkpoint.ConversationID != "" && upstreamStatus(err) != http.StatusBadRequest
		switch {
		case errors.Is(err, errImageTaskStorage):
			output.Error, output.ErrorCode = "任务状态存储失败，已停止继续提交", "storage_unavailable"
		case errors.Is(err, context.Canceled):
			output.Error, output.ErrorCode = "任务已中断，不会自动重新生成", "task_interrupted"
		case output.Resumable:
			output.Error, output.ErrorCode = "已提交图片，但结果尚未获取成功，可继续查询原任务", "image_poll_timeout"
		case provider.ImageSubmissionUncertain(err):
			output.Error, output.ErrorCode = "无法确认上游处理结果，不会自动重新生成，请先核对", "submission_unknown"
		}
		s.monitor.finish(output.CallID, "failed", task.Model, task.Mode, output.Error)
	}
	task.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	refreshImageTaskState(task)
	if task.Status == "success" || task.Status == "error" {
		task.Prompt = ""
		s.cleanupImageTaskInputDir(task.InputDir)
		task.InputDir = ""
	}
	s.imageTaskPending--
	_ = s.persistImageTasksLocked()
}

func (s *Server) loadImageTaskInputs(task imageTaskState) ([]provider.OpenAIImageInput, error) {
	inputs := make([]provider.OpenAIImageInput, 0, len(task.Inputs))
	for i, input := range task.Inputs {
		raw, err := os.ReadFile(filepath.Join(s.imageTaskInputsPath(), task.InputDir, fmt.Sprint(i)))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != input.Hash {
			return nil, errors.New("reference image changed after submission")
		}
		inputs = append(inputs, provider.OpenAIImageInput{Name: input.Name, MIME: input.MIME, Data: raw})
	}
	return inputs, nil
}

func (s *Server) resumeImageTaskOutput(r *http.Request, ctx context.Context, task imageTaskState, output imageTaskOutput) (data map[string]string, err error) {
	release, err := s.acquireImageSlot(ctx, r)
	if err != nil {
		return nil, err
	}
	defer release()
	lease, err := s.accountPool.ReserveExisting(ctx, func(account accounts.Account) bool {
		return isOpenAIAccount(account) && store.AccountID(account.Fields) == output.AccountID &&
			(output.UserID == "" || stringValue(account.Fields["user_id"]) == output.UserID) &&
			(output.WorkspaceID == "" || stringValue(account.Fields["account_id"]) == output.WorkspaceID)
	}, s.cfg.ImageAccountLimit)
	if err != nil {
		return nil, err
	}
	defer s.accountPool.Release(lease)
	defer func() { s.accountPool.Feedback(lease.Account, upstreamStatusOrOK(err), err) }()
	s.enrichMonitorAccount(r, lease.Account)
	ctx = s.monitorOpenAIImageContext(r, ctx)
	images, err := s.openAIImage.Resume(ctx, lease.Account, output.Checkpoint)
	if err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, errors.New("existing generation has no downloadable output")
	}
	value, url, err := s.openAIImage.Resolve(ctx, lease.Account, images[0], "url", s.cfg.ImageDataDir, task.PublicBase)
	if err != nil {
		return nil, err
	}
	s.recordGeneratedMedia(ctx, map[string]string{"url": url})
	return value, nil
}

func upstreamStatusOrOK(err error) int {
	if err == nil {
		return http.StatusOK
	}
	return upstreamStatus(err)
}
