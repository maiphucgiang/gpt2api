package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/accounts"
)

func (s *Server) survivalSnapshot() map[string]any {
	config := mapValue(s.registerStore.Get()["openai_survival"])
	s.survivalMu.RLock()
	status := cloneMap(s.survivalStatus)
	s.survivalMu.RUnlock()
	for key, value := range config {
		status[key] = value
	}
	return status
}

func (s *Server) runOpenAISurvival(w http.ResponseWriter) {
	started := s.startAccountSweep(false)
	status := http.StatusOK
	if started {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{"started": started, "survival": s.survivalSnapshot()})
}

func (s *Server) startAccountSweep(dueOnly bool) bool {
	s.survivalMu.Lock()
	if s.survivalRunning || s.backgroundContext.Err() != nil {
		s.survivalMu.Unlock()
		return false
	}
	s.survivalRunning = true
	s.survivalStatus["running"] = true
	s.survivalStatus["last_started_at"] = time.Now().UTC().Format(time.RFC3339)
	s.survivalStatus["last_error"] = ""
	s.backgroundWorkers.Add(1)
	s.survivalMu.Unlock()
	go func() { defer s.backgroundWorkers.Done(); s.executeAccountSweep(dueOnly) }()
	return true
}

func (s *Server) executeOpenAISurvival() { s.executeAccountSweep(false) }

func (s *Server) executeAccountSweep(dueOnly bool) {
	config := mapValue(s.registerStore.Get()["openai_survival"])
	concurrency := max(1, min(8, intValue(config["concurrency"])))
	interval := accountRefreshInterval(config)
	items, err := s.store.AccountList()
	if err != nil {
		s.finishSurvival(map[string]any{"error": err.Error()})
		return
	}
	jobs := make(chan map[string]any)
	counts := map[string]int{}
	var mu sync.Mutex
	var workers sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				status := s.survivalProbe(item, boolValue(config["refresh_codex_rt"], true))
				mu.Lock()
				counts[status]++
				mu.Unlock()
			}
		}()
	}
	now := time.Now()
	for _, item := range items {
		if !refreshableAccount(item) || (dueOnly && nextAccountRefresh(item, interval, now).After(now)) {
			continue
		}
		select {
		case jobs <- item:
		case <-s.backgroundContext.Done():
			break
		}
		if s.backgroundContext.Err() != nil {
			break
		}
	}
	close(jobs)
	workers.Wait()
	confirmed, failed := 0, 0
	for status, count := range counts {
		if isHealthyOpenAIPlan(status) {
			confirmed += count
		}
		if status == "error" || status == "token_dead" {
			failed += count
		}
	}
	s.finishSurvival(map[string]any{"total": sumCounts(counts), "confirmed": confirmed, "errors": failed, "statuses": counts})
}

func refreshableAccount(item map[string]any) bool {
	return boolValue(item["enabled"], true) && accountStatusCategory(item) != "disabled" && isOpenAIAccount(accounts.Account{Token: accountToken(item), Fields: item})
}

func accountRefreshInterval(config map[string]any) time.Duration {
	minutes := intValue(config["interval_minutes"])
	if minutes < 15 {
		minutes = 60
	}
	return time.Duration(minutes) * time.Minute
}

func nextAccountRefresh(item map[string]any, interval time.Duration, now time.Time) time.Time {
	if retry, err := time.Parse(time.RFC3339, stringValue(item["quota_refresh_retry_at"])); err == nil && retry.After(now) {
		return retry
	}
	last, err := time.Parse(time.RFC3339, firstNonEmpty(stringValue(item["last_quota_refresh_at"]), stringValue(item["survival_last_checked_at"])))
	if err != nil {
		return now
	}
	next := last.Add(interval)
	if intValue(item["quota"]) == 0 && !boolValue(item["image_quota_unknown"], false) {
		if reset, err := time.Parse(time.RFC3339, stringValue(item["restore_at"])); err == nil && reset.After(last) && reset.Before(next) {
			next = reset
		}
	}
	return next
}

func (s *Server) openAISurvivalScheduler() {
	defer s.backgroundWorkers.Done()
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-s.backgroundContext.Done():
			return
		case <-s.survivalWake:
		case <-timer.C:
		}
		config := mapValue(s.registerStore.Get()["openai_survival"])
		next := ""
		if boolValue(config["enabled"], true) {
			now := time.Now()
			items, err := s.store.AccountList()
			if err == nil {
				var earliest time.Time
				for _, item := range items {
					if refreshableAccount(item) {
						due := nextAccountRefresh(item, accountRefreshInterval(config), now)
						if earliest.IsZero() || due.Before(earliest) {
							earliest = due
						}
					}
				}
				if !earliest.IsZero() {
					next = earliest.UTC().Format(time.RFC3339)
					if !earliest.After(now) {
						s.startAccountSweep(true)
					}
				}
			}
		}
		s.survivalMu.Lock()
		s.survivalStatus["next_run_at"] = next
		s.survivalMu.Unlock()
		timer.Reset(30 * time.Second)
	}
}

func (s *Server) survivalProbe(account map[string]any, refreshFirst bool) string {
	ctx, cancel := context.WithTimeout(s.backgroundContext, 90*time.Second)
	defer cancel()
	rotate := refreshFirst && stringValue(account["refresh_token"]) != "" && stringValue(account["source_type"]) != "chatgpt_web"
	updated, err := s.refreshAccountSnapshotMode(ctx, accountPublicRef(account), rotate)
	if err != nil {
		status := "error"
		if strings.Contains(strings.ToLower(err.Error()), "invalid") || strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
			status = "token_dead"
		}
		return status
	}
	return firstNonEmpty(strings.ToLower(stringValue(updated["type"])), "free")
}

func (s *Server) finishSurvival(summary map[string]any) {
	s.survivalMu.Lock()
	defer s.survivalMu.Unlock()
	s.survivalRunning = false
	s.survivalStatus["running"] = false
	s.survivalStatus["last_finished_at"] = time.Now().UTC().Format(time.RFC3339)
	if value, ok := summary["error"]; ok {
		s.survivalStatus["last_error"] = value
	} else {
		s.survivalStatus["last_error"] = ""
		s.survivalStatus["last_summary"] = summary
	}
}

func isHealthyOpenAIPlan(status string) bool {
	switch status {
	case "free", "plus", "pro", "prolite", "team", "business", "enterprise", "edu", "k12":
		return true
	}
	return false
}
func sumCounts(values map[string]int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}
