package store

import (
	"context"
	"os"
	"testing"
	"time"

	"codecodriver/internal/domain"
)

func TestPostgresPersistence(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	data, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if _, err := data.pool.Exec(ctx, "TRUNCATE memory_entries,artifacts,task_steps,task_runs,tasks,symbols,repository_files,repositories CASCADE"); err != nil {
		t.Fatal(err)
	}
	defer data.pool.Exec(ctx, "TRUNCATE memory_entries,artifacts,task_steps,task_runs,tasks,symbols,repository_files,repositories CASCADE")

	now := time.Now().UTC().Truncate(time.Microsecond)
	repo := domain.Repository{ID: "repo-test", Name: "sample", Path: "/sample", CreatedAt: now}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	repo.PrimaryLanguage, repo.FileCount, repo.IndexedAt = "go", 1, now
	files := []domain.RepositoryFile{{RepositoryID: repo.ID, Path: "main.go", Language: "go", Size: 12, Hash: "hash", Summary: "package main"}}
	symbols := []domain.Symbol{{RepositoryID: repo.ID, FilePath: "main.go", Name: "main", Kind: "function", Line: 3}}
	if err := data.SetIndex(repo, files, symbols); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-test", RepositoryID: repo.ID, Title: "test", Description: "persist execution", Status: domain.TaskCreated, CreatedAt: now, UpdatedAt: now}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	run := domain.TaskRun{ID: "run-test", TaskID: task.ID, Status: domain.TaskPlanning, StartedAt: now}
	if err := data.AddRun(run); err != nil {
		t.Fatal(err)
	}
	step := domain.TaskStep{ID: "step-test", TaskID: task.ID, RunID: run.ID, AgentName: "planner", StepType: "PLANNING", Status: "COMPLETED", Input: map[string]any{"attempt": 1}, Output: map[string]any{"plan": "ok"}, StartedAt: now, EndedAt: now, LatencyMS: 5}
	if err := data.AddStep(step); err != nil {
		t.Fatal(err)
	}
	if err := data.AddToolCall(domain.ToolCall{ID: "tool-test", TaskID: task.ID, RunID: run.ID, StepID: step.ID, ToolName: "parse_document", ProviderType: "gateway", RequestPayload: map[string]any{"filename": "a.txt"}, ResponsePayload: map[string]any{"chunks": 1}, Status: "COMPLETED", StartedAt: now, EndedAt: now, LatencyMS: 4}); err != nil {
		t.Fatal(err)
	}
	if err := data.AddArtifact(domain.Artifact{ID: "artifact-test", TaskID: task.ID, RunID: run.ID, Type: "plan", Name: "plan.md", Content: "ok", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.AddMemory(domain.MemoryEntry{ID: "memory-test", RepositoryID: repo.ID, TaskID: task.ID, Kind: "summary", Content: "persistent memory", Source: "reviewer", Score: 2, Metadata: map[string]string{"decision": "approve"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.UpdateTask(task.ID, domain.TaskCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if err := data.FinishRun(task.ID, run.ID, domain.TaskCompleted); err != nil {
		t.Fatal(err)
	}

	if got, err := data.Repository(repo.ID); err != nil || got.FileCount != 1 {
		t.Fatalf("repository=%+v err=%v", got, err)
	}
	if got, err := data.Files(repo.ID); err != nil || len(got) != 1 {
		t.Fatalf("files=%+v err=%v", got, err)
	}
	if got, err := data.Symbols(repo.ID); err != nil || len(got) != 1 {
		t.Fatalf("symbols=%+v err=%v", got, err)
	}
	if got, err := data.Steps(task.ID); err != nil || len(got) != 1 {
		t.Fatalf("steps=%+v err=%v", got, err)
	}
	if got, err := data.ToolCalls(task.ID); err != nil || len(got) != 1 || got[0].ToolName != "parse_document" {
		t.Fatalf("tool_calls=%+v err=%v", got, err)
	}
	if got, err := data.Artifacts(task.ID); err != nil || len(got) != 1 {
		t.Fatalf("artifacts=%+v err=%v", got, err)
	}
	if got, err := data.SearchMemory(repo.ID, "persistent"); err != nil || len(got) != 1 || got[0].Source != "reviewer" || got[0].Metadata["decision"] != "approve" {
		t.Fatalf("memory=%+v err=%v", got, err)
	}
}
