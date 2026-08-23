package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	step := domain.TaskStep{ID: "step-report", TaskID: task.ID, RunID: run.ID, AgentName: "planner", StepType: "PLANNING", Status: "COMPLETED", StartedAt: time.Now(), EndedAt: time.Now()}
	if err := data.AddStep(step); err != nil {
		t.Fatal(err)
	}
	codebaseStep := domain.TaskStep{ID: "step-codebase-report", TaskID: task.ID, RunID: run.ID, AgentName: "codebase", StepType: "RETRIEVING_CONTEXT", Status: "COMPLETED", StartedAt: time.Now(), EndedAt: time.Now()}
	if err := data.AddStep(codebaseStep); err != nil {
		t.Fatal(err)
	}
	if err := data.AddToolCall(domain.ToolCall{ID: "tool-report", TaskID: task.ID, RunID: run.ID, StepID: codebaseStep.ID, ToolName: "read_file", ProviderType: "gateway", Status: "COMPLETED", StartedAt: time.Now(), EndedAt: time.Now(), LatencyMS: 4}); err != nil {
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
				Steps int `json:"steps"`
			} `json:"agents"`
			ToolUsage map[string]struct {
				Calls int `json:"calls"`
			} `json:"tool_usage"`
			Dimensions map[string]struct {
				Score float64 `json:"score"`
			} `json:"dimensions"`
			Trace struct {
				Phases map[string]struct {
					Tokens int `json:"tokens"`
				} `json:"phases"`
				Events []struct {
					Type  string `json:"type"`
					Phase string `json:"phase"`
				} `json:"events"`
			} `json:"trace"`
		} `json:"runs"`
		AgentStats map[string]struct {
			Calls int `json:"calls"`
			Steps int `json:"steps"`
		} `json:"agent_stats"`
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
	if report.Runs[0].Agents["codebase"].Steps != 1 || report.Runs[0].Agents["codebase"].Calls != 0 {
		t.Fatalf("run agents=%+v", report.Runs[0].Agents)
	}
	if report.AgentStats["planner"].Calls != 1 || report.AgentStats["codebase"].Steps != 1 {
		t.Fatalf("agent_stats=%+v", report.AgentStats)
	}
	if report.Runs[0].ToolUsage["read_file"].Calls != 1 {
		t.Fatalf("tool_usage=%+v", report.Runs[0].ToolUsage)
	}
	for _, key := range []string{"result_usability", "planning", "efficiency", "safety"} {
		if _, ok := report.Runs[0].Dimensions[key]; !ok {
			t.Fatalf("dimensions missing %s: %+v", key, report.Runs[0].Dimensions)
		}
	}
	if len(report.Runs[0].Trace.Events) < 3 || report.Runs[0].Trace.Phases["planning"].Tokens == 0 {
		t.Fatalf("trace=%+v", report.Runs[0].Trace)
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
