package runtime

import (
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
	if err := service.persistExecutionMemories(repo, task, "run-1", ReviewApprove, history, contextData); err != nil {
		t.Fatal(err)
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

func hasMemoryLink(links []domain.MemoryLink, targetType, targetID string) bool {
	for _, link := range links {
		if link.TargetType == targetType && link.TargetID == targetID {
			return true
		}
	}
	return false
}
