package server

import (
	"fmt"
	"net/http"
	"time"

	"codecodriver/internal/domain"
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
	passed := 0
	byMode := map[string]map[string]int{}
	byCase := map[string]map[string]map[string]int{}
	for _, run := range runs {
		if run.Passed {
			passed++
		}
		if byMode[run.Mode] == nil {
			byMode[run.Mode] = map[string]int{"total": 0, "passed": 0}
		}
		byMode[run.Mode]["total"]++
		if run.Passed {
			byMode[run.Mode]["passed"]++
		}
		if byCase[run.CaseID] == nil {
			byCase[run.CaseID] = map[string]map[string]int{}
		}
		if byCase[run.CaseID][run.Mode] == nil {
			byCase[run.CaseID][run.Mode] = map[string]int{"total": 0, "passed": 0}
		}
		byCase[run.CaseID][run.Mode]["total"]++
		if run.Passed {
			byCase[run.CaseID][run.Mode]["passed"]++
		}
	}
	rate := 0.0
	if len(runs) > 0 {
		rate = float64(passed) / float64(len(runs))
	}
	write(w, http.StatusOK, map[string]any{"cases": cases, "runs": runs, "batches": batches, "metrics": map[string]any{"total": len(runs), "passed": passed, "pass_rate": rate, "by_mode": byMode, "by_case": byCase}})
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
