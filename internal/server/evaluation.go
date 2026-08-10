package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/store"
)

func (s *Server) evaluations(w http.ResponseWriter, _ *http.Request) {
	cases, err := s.store.BenchmarkCases()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	runs, err := s.store.AllEvaluationRuns()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	batches, err := s.store.EvaluationBatches()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	history, err := s.store.EvaluationMetricSnapshots()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	passed, humanReview, failed := 0, 0, 0
	autoHuman := 0
	externalErrors := 0
	byMode := map[string]map[string]int{}
	byCase := map[string]map[string]map[string]int{}
	byMemory := map[string]map[string]any{}
	caseByID := map[string]domain.BenchmarkCase{}
	for _, item := range cases {
		caseByID[item.ID] = item
	}
	byCategory := map[string]map[string]int{}
	for _, run := range runs {
		autoCount := evalAutoHumanCount(run.Notes)
		autoHuman += autoCount
		if evalExternalError(run.Notes) {
			externalErrors++
		}
		if run.Passed {
			passed++
		}
		switch run.Status {
		case "human_review_required":
			humanReview++
		case "failed":
			failed++
		}
		if byMode[run.Mode] == nil {
			byMode[run.Mode] = map[string]int{"total": 0, "passed": 0, "human_review": 0, "failed": 0, "auto_human": 0}
		}
		byMode[run.Mode]["total"]++
		byMode[run.Mode]["auto_human"] += autoCount
		if run.Passed {
			byMode[run.Mode]["passed"]++
		}
		switch run.Status {
		case "human_review_required":
			byMode[run.Mode]["human_review"]++
		case "failed":
			byMode[run.Mode]["failed"]++
		}
		if byCase[run.CaseID] == nil {
			byCase[run.CaseID] = map[string]map[string]int{}
		}
		if byCase[run.CaseID][run.Mode] == nil {
			byCase[run.CaseID][run.Mode] = map[string]int{"total": 0, "passed": 0, "human_review": 0, "failed": 0, "auto_human": 0}
		}
		byCase[run.CaseID][run.Mode]["total"]++
		byCase[run.CaseID][run.Mode]["auto_human"] += autoCount
		if run.Passed {
			byCase[run.CaseID][run.Mode]["passed"]++
		}
		switch run.Status {
		case "human_review_required":
			byCase[run.CaseID][run.Mode]["human_review"]++
		case "failed":
			byCase[run.CaseID][run.Mode]["failed"]++
		}
		group := memoryGroup(run.Mode)
		if byMemory[group] == nil {
			byMemory[group] = map[string]any{"total": 0, "passed": 0, "human_review": 0, "failed": 0, "auto_human": 0, "duration_ms": int64(0), "memory_hits": 0, "repair_attempts": 0, "memory_success_hits": 0, "memory_failure_hits": 0, "memory_resolved_hits": 0, "memory_refined_hits": 0}
		}
		byMemory[group]["total"] = byMemory[group]["total"].(int) + 1
		byMemory[group]["auto_human"] = byMemory[group]["auto_human"].(int) + autoCount
		if run.Passed {
			byMemory[group]["passed"] = byMemory[group]["passed"].(int) + 1
		}
		switch run.Status {
		case "human_review_required":
			byMemory[group]["human_review"] = byMemory[group]["human_review"].(int) + 1
		case "failed":
			byMemory[group]["failed"] = byMemory[group]["failed"].(int) + 1
		}
		byMemory[group]["duration_ms"] = byMemory[group]["duration_ms"].(int64) + run.DurationMS
		byMemory[group]["memory_hits"] = byMemory[group]["memory_hits"].(int) + run.MemoryHits
		byMemory[group]["repair_attempts"] = byMemory[group]["repair_attempts"].(int) + run.RepairAttempts
		byMemory[group]["memory_success_hits"] = byMemory[group]["memory_success_hits"].(int) + run.MemorySuccessHits
		byMemory[group]["memory_failure_hits"] = byMemory[group]["memory_failure_hits"].(int) + run.MemoryFailureHits
		byMemory[group]["memory_resolved_hits"] = byMemory[group]["memory_resolved_hits"].(int) + run.MemoryResolvedHits
		byMemory[group]["memory_refined_hits"] = byMemory[group]["memory_refined_hits"].(int) + run.MemoryRefinedHits
		category := evalCategory(caseByID[run.CaseID].Name)
		if byCategory[category] == nil {
			byCategory[category] = map[string]int{"total": 0, "passed": 0, "human_review": 0, "failed": 0, "auto_human": 0}
		}
		byCategory[category]["total"]++
		byCategory[category]["auto_human"] += autoCount
		if run.Passed {
			byCategory[category]["passed"]++
		}
		switch run.Status {
		case "human_review_required":
			byCategory[category]["human_review"]++
		case "failed":
			byCategory[category]["failed"]++
		}
	}
	for _, metrics := range byMemory {
		total := metrics["total"].(int)
		if total > 0 {
			metrics["avg_duration_ms"] = metrics["duration_ms"].(int64) / int64(total)
		} else {
			metrics["avg_duration_ms"] = int64(0)
		}
		delete(metrics, "duration_ms")
	}
	rate := 0.0
	effectiveCompleted := passed + failed - externalErrors
	if effectiveCompleted > 0 {
		rate = float64(passed) / float64(effectiveCompleted)
	}
	write(w, http.StatusOK, map[string]any{"cases": cases, "runs": runs, "batches": batches, "history": history, "metrics": map[string]any{"total": len(runs), "passed": passed, "human_review": humanReview, "failed": failed, "auto_human": autoHuman, "external_errors": externalErrors, "pass_rate": rate, "by_mode": byMode, "by_case": byCase, "by_memory": byMemory, "by_category": byCategory}})
}

func evalAutoHumanCount(notes string) int {
	if notes == "" || !strings.HasPrefix(notes, "{") {
		return 0
	}
	var payload struct {
		AutoHuman []string `json:"auto_human"`
	}
	if err := json.Unmarshal([]byte(notes), &payload); err != nil {
		return 0
	}
	return len(payload.AutoHuman)
}

func evalCategory(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "explain"):
		return "explanation"
	case strings.Contains(lower, "security"):
		return "security"
	case strings.Contains(lower, "readme") || strings.Contains(lower, "document"):
		return "documentation"
	case strings.Contains(lower, "refactor"):
		return "refactor"
	case strings.Contains(lower, "test") || strings.Contains(lower, "coverage") || strings.Contains(lower, "logging") || strings.Contains(lower, "health") || strings.Contains(lower, "pagination"):
		return "test"
	default:
		return "code"
	}
}

func evalExternalError(notes string) bool {
	lower := strings.ToLower(notes)
	return strings.Contains(lower, "insufficient balance") || strings.Contains(lower, "status 402") || strings.Contains(lower, "402")
}

func memoryGroup(mode string) string {
	if mode == domain.MemoryModeWithout || mode == "baseline" {
		return domain.MemoryModeWithout
	}
	return domain.MemoryModeWith
}

func (s *Server) createEvaluationSuite(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name    string   `json:"name"`
		Mode    string   `json:"mode"`
		CaseIDs []string `json:"case_ids"`
	}
	if err := decode(r, &request); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	cases, err := s.store.BenchmarkCases()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	selected := map[string]bool{}
	for _, id := range request.CaseIDs {
		selected[id] = true
	}
	if len(selected) == 0 {
		for _, item := range cases {
			selected[item.ID] = true
		}
	}
	chosen := []domain.BenchmarkCase{}
	for _, item := range cases {
		if selected[item.ID] {
			chosen = append(chosen, item)
		}
	}
	if len(chosen) == 0 {
		problem(w, http.StatusBadRequest, fmt.Errorf("no benchmark cases selected"))
		return
	}
	if request.Mode == "" {
		request.Mode = "agent"
	}
	if request.Name == "" {
		request.Name = "benchmark suite"
	}
	now := time.Now().UTC()
	id, err := s.store.ID("batch")
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	batch := domain.EvaluationBatch{ID: id, Name: request.Name, Mode: request.Mode, Status: "running", Total: len(chosen), StartedAt: now, CreatedAt: now}
	if err := s.store.AddEvaluationBatch(batch); err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	runs := []domain.EvaluationRun{}
	tasks := []domain.Task{}
	for _, item := range chosen {
		run, task, createErr := s.runtime.CreateEvaluationTask(item.ID, request.Mode, batch.ID)
		if createErr != nil {
			batch.Status = "failed"
			_ = s.store.UpdateEvaluationBatch(batch)
			problem(w, http.StatusBadRequest, createErr)
			return
		}
		runs = append(runs, run)
		tasks = append(tasks, task)
	}
	write(w, http.StatusAccepted, map[string]any{"batch": batch, "runs": runs, "tasks": tasks})
}

func (s *Server) createBenchmarkCase(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name         string   `json:"name"`
		RepositoryID string   `json:"repository_id"`
		Title        string   `json:"title"`
		Description  string   `json:"description"`
		Expected     []string `json:"expected"`
	}
	if err := decode(r, &request); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.store.Repository(request.RepositoryID); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	id, err := s.store.ID("benchmark")
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	item := domain.BenchmarkCase{ID: id, Name: request.Name, RepositoryID: request.RepositoryID, Title: request.Title, Description: request.Description, Expected: request.Expected, CreatedAt: time.Now().UTC()}
	if err := s.store.AddBenchmarkCase(item); err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	write(w, http.StatusCreated, item)
}

func (s *Server) updateBenchmarkCase(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RepositoryID string   `json:"repository_id"`
		Name         string   `json:"name"`
		Title        string   `json:"title"`
		Description  string   `json:"description"`
		Expected     []string `json:"expected"`
	}
	if err := decode(r, &request); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.store.BenchmarkCase(r.PathValue("id"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		problem(w, status, err)
		return
	}
	if request.Name != "" {
		item.Name = request.Name
	}
	if request.RepositoryID != "" {
		item.RepositoryID = request.RepositoryID
	}
	if request.Title != "" {
		item.Title = request.Title
	}
	if request.Description != "" {
		item.Description = request.Description
	}
	if request.Expected != nil {
		item.Expected = request.Expected
	}
	if err := s.store.UpdateBenchmarkCase(item); err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	write(w, http.StatusOK, item)
}

func (s *Server) createEvaluationRun(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CaseID     string    `json:"case_id"`
		TaskID     string    `json:"task_id"`
		Mode       string    `json:"mode"`
		Status     string    `json:"status"`
		Notes      string    `json:"notes"`
		Passed     bool      `json:"passed"`
		DurationMS int64     `json:"duration_ms"`
		StartedAt  time.Time `json:"started_at"`
		EndedAt    time.Time `json:"ended_at"`
	}
	if err := decode(r, &request); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.store.BenchmarkCase(request.CaseID); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	if request.TaskID == "" && request.Status == "" {
		run, task, err := s.runtime.CreateEvaluationTask(request.CaseID, request.Mode)
		if err != nil {
			problem(w, http.StatusBadRequest, err)
			return
		}
		write(w, http.StatusAccepted, map[string]any{"evaluation": run, "task": task})
		return
	}
	now := time.Now().UTC()
	if request.StartedAt.IsZero() {
		request.StartedAt = now
	}
	if request.EndedAt.IsZero() {
		request.EndedAt = now
	}
	id, err := s.store.ID("evaluation")
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	run := domain.EvaluationRun{ID: id, CaseID: request.CaseID, TaskID: request.TaskID, Mode: request.Mode, Status: request.Status, Passed: request.Passed, DurationMS: request.DurationMS, Notes: request.Notes, StartedAt: request.StartedAt, EndedAt: request.EndedAt, CreatedAt: now}
	if run.Mode == "" {
		run.Mode = "agent"
	}
	if run.Status == "" {
		run.Status = "completed"
	}
	if err := s.store.AddEvaluationRun(run); err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	write(w, http.StatusCreated, run)
}
