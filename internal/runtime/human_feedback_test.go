package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/sandbox"
	"codecodriver/internal/store"
)

func TestContinueHumanReviewWithFeedbackRequeuesTask(t *testing.T) {
	data := store.NewMemory()
	task := domain.Task{ID: "task-feedback", RepositoryID: "repo-1", Status: domain.TaskHumanReview, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	service := &Service{store: data, queue: make(chan string, 1)}
	got, err := service.ContinueTaskWithFeedback(task.ID, "Re-run validation with go test ./internal/auth/")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskCreated {
		t.Fatalf("status=%s", got.Status)
	}
	select {
	case queued := <-service.queue:
		if queued != task.ID {
			t.Fatalf("queued=%s", queued)
		}
	default:
		t.Fatal("task was not enqueued after feedback")
	}
	artifacts, err := data.Artifacts(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasArtifactType(artifacts, "human_feedback") {
		t.Fatalf("human feedback artifact missing: %+v", artifacts)
	}
}

func TestHumanFeedbackInjectedIntoPlannerPrompt(t *testing.T) {
	fake := &recordingLLM{responses: []string{"plan"}}
	request := AgentRequest{
		Task:       domain.Task{Title: "security audit", Description: "review auth"},
		Repository: domain.Repository{Name: "sample"},
		Context: map[string]any{
			"human_feedback":  "Re-run validation with go test ./internal/auth/",
			"previous_review": "The affected package tests were not executed.",
			"previous_patch":  "--- a/internal/auth/api.go\n+++ b/internal/auth/api.go",
		},
	}
	if _, err := (PlannerAgent{LLM: fake}).Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	prompt := fake.prompts[0]
	if !strings.Contains(prompt, "HUMAN FEEDBACK") || !strings.Contains(prompt, "go test ./internal/auth/") {
		t.Fatalf("prompt missing human feedback: %s", prompt)
	}
	if !strings.Contains(prompt, "PREVIOUS REVIEW") || !strings.Contains(prompt, "PREVIOUS PATCH") {
		t.Fatalf("prompt missing previous evidence: %s", prompt)
	}
}

func TestExecuteLoadsHumanFeedbackContext(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-feedback", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-feedback-exec", RepositoryID: repo.ID, Title: "security audit", Description: "review auth", Status: domain.TaskCreated, MemoryMode: domain.MemoryModeWith, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	previousRun := domain.TaskRun{ID: "run-feedback-prev", TaskID: task.ID, Status: domain.TaskHumanReview, StartedAt: time.Now(), EndedAt: time.Now()}
	if err := data.AddRun(previousRun); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := data.AddArtifact(domain.Artifact{ID: "review-feedback", TaskID: task.ID, RunID: previousRun.ID, Type: "review", Name: "review.md", Content: "affected package tests not executed", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.AddArtifact(domain.Artifact{ID: "feedback-feedback", TaskID: task.ID, RunID: previousRun.ID, Type: "human_feedback", Name: "human-feedback.json", Content: `{"feedback":"Re-run go test ./internal/auth/"}`, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}}
	codebase := &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}
	patch := &sequenceAgent{name: "patch", results: []AgentResult{{Output: map[string]any{"proposal": "--- a/auth.go\n+++ b/auth.go\n@@ -1 +1 @@\n-old\n+new\n"}}}}
	testAgent := &sequenceAgent{name: "test", results: []AgentResult{{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}}}}
	reviewer := &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: map[string]any{"decision": ReviewApprove}}}}
	service := &Service{store: data, indexer: indexer.New(), queue: make(chan string, 1), planner: planner, codebase: codebase, patch: patch, test: testAgent, reviewer: reviewer}
	service.execute(context.Background(), task.ID)

	if len(planner.requests) == 0 {
		t.Fatal("planner did not run")
	}
	if got, ok := planner.requests[0].Context["human_feedback"].(string); !ok || !strings.Contains(got, "go test ./internal/auth/") {
		t.Fatalf("human_feedback=%v ok=%v", planner.requests[0].Context["human_feedback"], ok)
	}
	if got, ok := planner.requests[0].Context["previous_review"].(string); !ok || !strings.Contains(got, "affected package tests") {
		t.Fatalf("previous_review=%v ok=%v", planner.requests[0].Context["previous_review"], ok)
	}
	if got, ok := planner.requests[0].Context["test_command_override"].(string); !ok || !strings.Contains(got, "go test ./internal/auth/") {
		t.Fatalf("test_command_override=%v ok=%v", planner.requests[0].Context["test_command_override"], ok)
	}
}

func TestExtractTestCommandFromFeedback(t *testing.T) {
	cases := []struct {
		feedback string
		want     string
	}{
		{"Re-run validation with go test ./internal/auth/ before approving.", "go test ./internal/auth/"},
		{"Please run:\ngo test ./...\nand confirm.", "go test ./..."},
		{"No command here", ""},
	}
	for _, item := range cases {
		if got := extractTestCommandFromFeedback(item.feedback); got != item.want {
			t.Fatalf("feedback=%q got=%q want=%q", item.feedback, got, item.want)
		}
	}
}

func hasArtifactType(artifacts []domain.Artifact, artifactType string) bool {
	for _, artifact := range artifacts {
		if artifact.Type == artifactType {
			return true
		}
	}
	return false
}
