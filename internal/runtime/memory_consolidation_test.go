package runtime

import (
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
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
	contextData := map[string]any{"codebase": map[string]any{"files": []string{"internal/llm/deepseek.go"}}}
	if err := service.persistExecutionMemories(repo.ID, task, "run-1", ReviewApprove, history, contextData); err != nil {
		t.Fatal(err)
	}
	memories, err := data.SearchMemoryLimit(repo.ID, "retry", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 3 {
		t.Fatalf("memories=%+v", memories)
	}
	if memories[0].Kind == "" || memories[0].Metadata["run_id"] != "run-1" {
		t.Fatalf("metadata=%+v", memories)
	}
}
