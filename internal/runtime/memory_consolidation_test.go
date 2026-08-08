package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/sandbox"
	"codecodriver/internal/store"
)

func TestPersistExecutionMemories(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-memory", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	service := NewService(data, indexer.New())
	task := domain.Task{ID: "task-memory", RepositoryID: repo.ID, Title: "retry task", Description: "retry task", Status: domain.TaskCompleted, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	history := []map[string]any{{"attempt": 1, "status": "apply_failed", "error": "corrupt patch"}, {"attempt": 2, "status": "passed", "applied": true, "passed": true}}
	contextData := map[string]any{
		"codebase": map[string]any{"files": []string{"internal/llm/deepseek.go"}},
		"patch":    map[string]any{"proposal": "--- a/internal/llm/deepseek.go\n+++ b/internal/llm/deepseek.go\n"},
		"test":     sandbox.Report{Status: "passed", Applied: true, Passed: true, Output: "ok"},
	}
	created, err := service.persistExecutionMemories(repo, task, "run-1", ReviewApprove, history, contextData)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 3 {
		t.Fatalf("created=%d", len(created))
	}
	memories, err := data.SearchMemoryLimit(repo.ID, "retry", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 3 {
		t.Fatalf("memories=%+v", memories)
	}
	var success, failure *domain.MemoryEntry
	for i := range memories {
		if memories[i].Kind == "execution_success" {
			success = &memories[i]
		}
		if memories[i].Kind == "failure_pattern" {
			failure = &memories[i]
		}
	}
	if success == nil || success.SuccessScore != 1 || len(success.ChangedFiles) != 1 || success.ChangedFiles[0] != "internal/llm/deepseek.go" {
		t.Fatalf("success=%+v", success)
	}
	if !strings.Contains(success.VerificationEvidence, "test_status") || !strings.Contains(success.VerificationEvidence, "internal/llm/deepseek.go") {
		t.Fatalf("verification_evidence=%q", success.VerificationEvidence)
	}
	if failure == nil || failure.Symptom != "corrupt patch" || failure.RootCause != "patch apply failure" || failure.SourceRunID != "run-1" {
		t.Fatalf("failure=%+v", failure)
	}
	if !hasMemoryLink(success.Links, "file", "internal/llm/deepseek.go") || !hasMemoryLink(success.Links, "task", task.ID) || !hasMemoryLink(success.Links, "run", "run-1") {
		t.Fatalf("success links=%+v", success.Links)
	}
}

func TestFailForRunPersistsFailureMemory(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-fail", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-fail", RepositoryID: repo.ID, Title: "fix retry", Description: "fix retry", Status: domain.TaskCreated, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	service := NewService(data, indexer.New())
	service.failForRun(task, "run-fail", 0, errors.New("planner unavailable"))
	memories, err := data.SearchMemoryLimit(repo.ID, "planner unavailable", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 || memories[0].Kind != "failure_pattern" || memories[0].Metadata["stage"] != "agent_loop" || memories[0].SourceRunID != "run-fail" {
		t.Fatalf("memories=%+v", memories)
	}
	if !hasMemoryLink(memories[0].Links, "task", task.ID) || !hasMemoryLink(memories[0].Links, "run", "run-fail") {
		t.Fatalf("links=%+v", memories[0].Links)
	}
}

func TestAsyncMemoryRefinementRecoversOnStart(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-async", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	raw := domain.MemoryEntry{ID: "raw-async", RepositoryID: repo.ID, TaskID: "task-async", Kind: "execution_success", Title: "fix retry", Content: "retry backoff passed", Summary: "retry backoff passed", Symptom: "timeout", RootCause: "fixed interval", SuccessScore: 1, CreatedAt: time.Now()}
	if err := data.AddMemory(raw); err != nil {
		t.Fatal(err)
	}
	fake := &recordingLLM{responses: []string{`{"summary":"use exponential backoff","root_cause":"fixed interval"}`}}
	service := NewServiceWithLLM(data, indexer.New(), fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for {
		entries, err := data.UnrefinedMemories(10)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("memory not refined after deadline: %+v", entries)
		}
		time.Sleep(20 * time.Millisecond)
	}
	memories, err := data.SearchMemoryLimit(repo.ID, "retry", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, memory := range memories {
		if memory.Kind == "refined_execution_success" && memory.Summary == "use exponential backoff" {
			return
		}
	}
	t.Fatalf("refined memory not found: %+v", memories)
}

func TestMemoryModeABHarness(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-ab", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	if err := data.SetIndex(repo, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := data.AddMemory(domain.MemoryEntry{ID: "ab-memory", RepositoryID: repo.ID, Kind: "execution_success", Title: "retry timeout", Content: "use exponential backoff", Summary: "use exponential backoff", Symptom: "timeout", RootCause: "fixed interval", ChangedFiles: []string{"retry.go"}, SuccessScore: 1, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	run := func(taskID, memoryMode string) []AgentRequest {
		task := domain.Task{ID: taskID, RepositoryID: repo.ID, Title: "retry timeout", Description: "handle retry timeout", Status: domain.TaskCreated, MemoryMode: memoryMode, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := data.AddTask(task); err != nil {
			t.Fatal(err)
		}
		planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}}
		codebase := &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}
		patch := &sequenceAgent{name: "patch", results: []AgentResult{{Output: map[string]any{"proposal": "patch"}}}}
		testAgent := &sequenceAgent{name: "test", results: []AgentResult{{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}}}}
		reviewer := &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: map[string]any{"decision": ReviewApprove}}}}
		service := &Service{store: data, indexer: indexer.New(), queue: make(chan string, 1), planner: planner, codebase: codebase, patch: patch, test: testAgent, reviewer: reviewer}
		service.execute(context.Background(), taskID)
		return planner.requests
	}

	withMemory := run("task-ab-with", domain.MemoryModeWith)
	if len(withMemory) == 0 {
		t.Fatal("with_memory planner did not run")
	}
	withHits, _ := withMemory[0].Context["memory_hits"].(int)
	if len(withMemory[0].Context["memory"].([]domain.MemoryEntry)) != 1 || withHits != 1 {
		t.Fatalf("with_memory hits=%d context=%+v", withHits, withMemory[0].Context["memory"])
	}

	withoutMemory := run("task-ab-without", domain.MemoryModeWithout)
	if len(withoutMemory) == 0 {
		t.Fatal("without_memory planner did not run")
	}
	withoutHits, _ := withoutMemory[0].Context["memory_hits"].(int)
	if len(withoutMemory[0].Context["memory"].([]domain.MemoryEntry)) != 0 || withoutHits != 0 {
		t.Fatalf("without_memory hits=%d context=%+v", withoutHits, withoutMemory[0].Context["memory"])
	}
}

func TestMemoryModeForEvaluation(t *testing.T) {
	if got := memoryModeForEvaluation("agent"); got != domain.MemoryModeWith {
		t.Fatalf("agent -> %s", got)
	}
	if got := memoryModeForEvaluation("without_memory"); got != domain.MemoryModeWithout {
		t.Fatalf("without_memory -> %s", got)
	}
	if got := memoryModeForEvaluation("baseline"); got != domain.MemoryModeWithout {
		t.Fatalf("baseline -> %s", got)
	}
}

func TestFinalizeEvaluationRecordsMemoryMetrics(t *testing.T) {
	data := store.NewMemory()
	now := time.Now()
	task := domain.Task{ID: "task-metrics", RepositoryID: "repo-metrics", Title: "retry", Description: "retry", MemoryMode: domain.MemoryModeWith, CreatedAt: now, UpdatedAt: now}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	if err := data.AddEvaluationRun(domain.EvaluationRun{ID: "eval-metrics", CaseID: "case", TaskID: task.ID, Mode: "with_memory", Status: "queued", StartedAt: now, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	service := NewService(data, indexer.New())
	service.finalizeEvaluation(task, domain.TaskCompleted, map[string]any{"memory_hits": 3, "repair_attempts": 2, "memory_success_hits": 1, "memory_failure_hits": 2, "memory_resolved_hits": 1, "memory_refined_hits": 1})
	runs, err := data.AllEvaluationRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || !runs[0].Passed || runs[0].MemoryHits != 3 || runs[0].RepairAttempts != 2 || runs[0].MemorySuccessHits != 1 || runs[0].MemoryFailureHits != 2 || runs[0].MemoryResolvedHits != 1 || runs[0].MemoryRefinedHits != 1 {
		t.Fatalf("runs=%+v", runs)
	}
	service.finalizeEvaluation(task, domain.TaskHumanReview, nil)
	runs, err = data.AllEvaluationRuns()
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].MemoryHits != 3 || runs[0].RepairAttempts != 2 || runs[0].MemorySuccessHits != 1 || runs[0].MemoryFailureHits != 2 || runs[0].MemoryResolvedHits != 1 || runs[0].MemoryRefinedHits != 1 {
		t.Fatalf("metrics reset after nil finalize: %+v", runs[0])
	}
}

func hasMemoryLink(links []domain.MemoryLink, targetType, targetID string) bool {
	for _, link := range links {
		if link.TargetType == targetType && link.TargetID == targetID {
			return true
		}
	}
	return false
}
