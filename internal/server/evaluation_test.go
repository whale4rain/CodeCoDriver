package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/runtime"
	"codecodriver/internal/store"
)

func TestCreateEvaluationRunQueuesRealTask(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-eval", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	if err := data.AddBenchmarkCase(domain.BenchmarkCase{ID: "case-eval", Name: "smoke", RepositoryID: repo.ID, Title: "smoke", Description: "run smoke", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewService(data, indexer.New())
	engine.Start(context.Background())
	handler := New(data, engine)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/evaluations/runs", strings.NewReader(`{"case_id":"case-eval","mode":"agent"}`))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Evaluation domain.EvaluationRun `json:"evaluation"`
		Task       domain.Task          `json:"task"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Evaluation.TaskID != response.Task.ID || response.Evaluation.Status != "queued" {
		t.Fatalf("response=%+v", response)
	}
	runs, err := data.AllEvaluationRuns()
	if err != nil || len(runs) != 1 || runs[0].TaskID != response.Task.ID {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}

func TestUpdateBenchmarkCaseEndpoint(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-eval", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	if err := data.AddBenchmarkCase(domain.BenchmarkCase{ID: "case-update", Name: "old-name", RepositoryID: repo.ID, Title: "old title", Description: "old description", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/evaluations/cases/case-update", strings.NewReader(`{"name":"new-name","title":"new title","description":"new description"}`))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	item, err := data.BenchmarkCase("case-update")
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "new-name" || item.Description != "new description" {
		t.Fatalf("item=%+v", item)
	}
}

func TestEvaluationMetricsSeparateHumanReview(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-metrics", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	if err := data.AddBenchmarkCase(domain.BenchmarkCase{ID: "case-metrics", Name: "explain-metrics", RepositoryID: repo.ID, Title: "metrics", Description: "metrics", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	runs := []domain.EvaluationRun{
		{ID: "run-completed", CaseID: "case-metrics", Mode: "with_memory", Status: "completed", Passed: true, DurationMS: 100, StartedAt: now, CreatedAt: now},
		{ID: "run-human", CaseID: "case-metrics", Mode: "with_memory", Status: "human_review_required", Passed: false, DurationMS: 200, Notes: `{"auto_human":["auto_approved"]}`, StartedAt: now, CreatedAt: now},
		{ID: "run-failed", CaseID: "case-metrics", Mode: "with_memory", Status: "failed", Passed: false, DurationMS: 300, StartedAt: now, CreatedAt: now},
	}
	for _, run := range runs {
		if err := data.AddEvaluationRun(run); err != nil {
			t.Fatal(err)
		}
	}
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/evaluations", nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Metrics struct {
			Total       int                       `json:"total"`
			Passed      int                       `json:"passed"`
			HumanReview int                       `json:"human_review"`
			Failed      int                       `json:"failed"`
			AutoHuman   int                       `json:"auto_human"`
			PassRate    float64                   `json:"pass_rate"`
			ByCategory  map[string]map[string]int `json:"by_category"`
		} `json:"metrics"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Metrics.Total != 3 || response.Metrics.Passed != 1 || response.Metrics.HumanReview != 1 || response.Metrics.Failed != 1 {
		t.Fatalf("metrics=%+v", response.Metrics)
	}
	if response.Metrics.PassRate != 0.5 {
		t.Fatalf("pass_rate=%v", response.Metrics.PassRate)
	}
	if response.Metrics.AutoHuman != 1 {
		t.Fatalf("auto_human=%d", response.Metrics.AutoHuman)
	}
	if response.Metrics.ByCategory["explanation"]["auto_human"] != 1 {
		t.Fatalf("by_category=%+v", response.Metrics.ByCategory)
	}
}

func TestEvaluationReportIncludesAgentTokensAndScore(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-report", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	benchmark := domain.BenchmarkCase{ID: "case-report", Name: "explain-pagination-architecture", RepositoryID: repo.ID, Title: "Explain pagination", Description: "Explain pagination", Expected: []string{"pkg/pagination"}, CreatedAt: time.Now()}
	if err := data.AddBenchmarkCase(benchmark); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-report", RepositoryID: repo.ID, Status: domain.TaskCompleted, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	run := domain.EvaluationRun{ID: "run-report", CaseID: benchmark.ID, TaskID: task.ID, Mode: "agent", Status: "completed", Passed: true, DurationMS: 1200, RepairAttempts: 0, StartedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddEvaluationRun(run); err != nil {
		t.Fatal(err)
	}
	step := domain.TaskStep{ID: "step-report", TaskID: task.ID, RunID: run.ID, AgentName: "planner", Status: "COMPLETED", StartedAt: time.Now(), EndedAt: time.Now()}
	if err := data.AddStep(step); err != nil {
		t.Fatal(err)
	}
	if err := data.AddLLMUsage(domain.LLMUsage{ID: "llm-report", TaskID: task.ID, RunID: run.ID, StepID: step.ID, AgentName: "planner", Model: "deepseek-v4-flash", PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, EstimatedCostUSD: 0.01, LatencyMS: 20, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := data.AddArtifact(domain.Artifact{ID: "explain-report", TaskID: task.ID, RunID: run.ID, Type: "explanation", Name: "explanation.md", Content: "# Pagination\n\npkg/pagination implementation path details", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/evaluations/report", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var report struct {
		Summary struct {
			TotalRuns int `json:"total_runs"`
		} `json:"summary"`
		Runs []struct {
			Category     string  `json:"category"`
			QualityScore float64 `json:"quality_score"`
			TokenUsage   struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"token_usage"`
			Agents map[string]struct {
				Calls int `json:"calls"`
			} `json:"agents"`
		} `json:"runs"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.Summary.TotalRuns != 1 || len(report.Runs) != 1 {
		t.Fatalf("report=%+v", report)
	}
	if report.Runs[0].Category != "explanation" || report.Runs[0].QualityScore <= 0 {
		t.Fatalf("run=%+v", report.Runs[0])
	}
	if report.Runs[0].TokenUsage.TotalTokens != 150 || report.Runs[0].Agents["planner"].Calls != 1 {
		t.Fatalf("run=%+v", report.Runs[0])
	}
}

func TestScoreEvalRunRewardsTaskQualityOverCompletion(t *testing.T) {
	benchmark := domain.BenchmarkCase{Expected: []string{"pkg/pagination"}}
	highQualityScore, highQuality := scoreEvalRun(
		domain.EvaluationRun{Status: "completed", Passed: true, RepairAttempts: 1},
		benchmark,
		"test",
		5000,
		1,
		evalArtifactStats{PatchBytes: 1200},
		[]string{"pkg/pagination/pages.go"},
		false,
	)
	brokenScore, completedButBroken := scoreEvalRun(
		domain.EvaluationRun{Status: "completed", Passed: false, RepairAttempts: 3},
		benchmark,
		"test",
		10000,
		3,
		evalArtifactStats{PatchBytes: 1200},
		[]string{"src/main.py"},
		false,
	)
	if highQuality["completion"] != 20 || highQuality["deliverable"] != 60 {
		t.Fatalf("high quality breakdown=%+v", highQuality)
	}
	if highQualityScore != 90 {
		t.Fatalf("high quality score=%v breakdown=%+v", highQualityScore, highQuality)
	}
	if brokenScore >= 60 {
		t.Fatalf("broken completed run scored too high: %+v", completedButBroken)
	}
	if completedButBroken["repair_efficiency"] != 0 || completedButBroken["token_efficiency"] != 0 {
		t.Fatalf("broken run should not get efficiency points: %+v", completedButBroken)
	}
	if completedButBroken["completion"] != 0 {
		t.Fatalf("broken run should not get completion points: %+v", completedButBroken)
	}

	passedWrongPathScore, passedWrongPath := scoreEvalRun(
		domain.EvaluationRun{Status: "completed", Passed: true, RepairAttempts: 1},
		benchmark,
		"test",
		5000,
		1,
		evalArtifactStats{PatchBytes: 1200},
		[]string{"src/main.py"},
		false,
	)
	if passedWrongPathScore > 60 {
		t.Fatalf("passed run with wrong path scored too high: score=%v breakdown=%+v", passedWrongPathScore, passedWrongPath)
	}
}

func TestApplyTaskPatchEndpoint(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "original.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "CodeCoDriver Test"},
		{"add", "original.txt"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	now := time.Now()
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-apply", Name: "apply", Path: root, CreatedAt: now}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-apply", RepositoryID: repo.ID, Title: "add file", Description: "add file", Status: domain.TaskCompleted, CreatedAt: now, UpdatedAt: now}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	if err := data.AddRun(domain.TaskRun{ID: "run-apply-live", TaskID: task.ID, Status: domain.TaskCompleted, StartedAt: now, EndedAt: now}); err != nil {
		t.Fatal(err)
	}
	proposal := "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1,2 @@\n+new\n+\n"
	if err := data.AddArtifact(domain.Artifact{ID: "artifact-apply", TaskID: task.ID, RunID: "run-apply", Type: "patch_proposal", Name: "attempt-1-proposed-change.diff", Content: proposal, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/tasks/task-apply/apply", strings.NewReader("{}"))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Status       string   `json:"status"`
		AppliedFiles []string `json:"applied_files"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.AppliedFiles) != 1 || response.AppliedFiles[0] != "new.txt" {
		t.Fatalf("response=%+v", response)
	}
	artifacts, err := data.Artifacts(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundAppliedArtifact := false
	for _, artifact := range artifacts {
		if artifact.Type == "applied_patch" && artifact.RunID == "run-apply-live" {
			foundAppliedArtifact = true
		}
	}
	if !foundAppliedArtifact {
		t.Fatalf("applied_patch artifact missing: %+v", artifacts)
	}
	updatedRepo, err := data.Repository(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedRepo.IndexedAt.IsZero() {
		t.Fatalf("repository index not refreshed: %+v", updatedRepo)
	}
	memories, err := data.SearchMemoryLimit(repo.ID, "Applied task patch", 10)
	if err != nil {
		t.Fatal(err)
	}
	foundMemory := false
	for _, memory := range memories {
		if memory.Kind == "execution_success" && memory.Source == "applier" {
			foundMemory = true
			break
		}
	}
	if !foundMemory {
		t.Fatalf("apply success memory not found: %+v", memories)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/tasks/task-apply/apply", strings.NewReader("{}")))
	if recorder.Code != http.StatusOK {
		t.Fatalf("reapply status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "already_applied" {
		t.Fatalf("reapply response=%+v", response)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestRerunTaskEndpoint(t *testing.T) {
	now := time.Now()
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-rerun", Name: "rerun", Path: t.TempDir(), CreatedAt: now}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-rerun-old", RepositoryID: repo.ID, Title: "retry again", Description: "retry again", Status: domain.TaskFailed, CreatedAt: now, UpdatedAt: now}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/tasks/task-rerun-old/rerun", strings.NewReader("{}"))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var rerun domain.Task
	if err := json.NewDecoder(recorder.Body).Decode(&rerun); err != nil {
		t.Fatal(err)
	}
	if rerun.ID == task.ID || rerun.Title != task.Title {
		t.Fatalf("rerun=%+v", rerun)
	}
}
