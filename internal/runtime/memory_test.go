package runtime

import (
	"context"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/sandbox"
	"codecodriver/internal/store"
)

func TestMemoryIsPassedToPlanner(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-memory", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	_ = data.AddRepository(repo)
	_ = data.SetIndex(repo, nil, nil)
	_ = data.AddMemory(domain.MemoryEntry{ID: "memory-1", RepositoryID: repo.ID, Kind: "failure", Content: "retry timeout pattern", Source: "task-old", CreatedAt: time.Now()})
	task := domain.Task{ID: "task-memory", RepositoryID: repo.ID, Title: "retry timeout", Description: "handle retry timeout", Status: domain.TaskCreated, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = data.AddTask(task)
	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}}
	service := &Service{store: data, indexer: indexer.New(), queue: make(chan string, 1), workers: 1, planner: planner, codebase: &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}, patch: &sequenceAgent{name: "patch", results: []AgentResult{{Output: map[string]any{"proposal": "patch"}}}}, test: &sequenceAgent{name: "test", results: []AgentResult{{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}}}}, reviewer: &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: map[string]any{"decision": ReviewApprove}}}}}
	service.execute(context.Background(), task.ID)
	if len(planner.requests) == 0 {
		t.Fatal("planner did not run")
	}
	if memories, ok := planner.requests[0].Context["memory"]; !ok || len(memories.([]domain.MemoryEntry)) != 1 {
		t.Fatalf("memory context=%v", planner.requests[0].Context["memory"])
	}
}

func TestMemorySearchScoresAndLimits(t *testing.T) {
	data := store.NewMemory()
	_ = data.AddMemory(domain.MemoryEntry{ID: "a", RepositoryID: "repo", Content: "retry timeout backoff", CreatedAt: time.Now()})
	_ = data.AddMemory(domain.MemoryEntry{ID: "b", RepositoryID: "repo", Content: "retry only", CreatedAt: time.Now()})
	got, err := data.SearchMemoryLimit("repo", "retry timeout", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" || got[0].Score != 2 {
		t.Fatalf("results=%+v", got)
	}
}
