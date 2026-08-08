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

type sequenceAgent struct {
	name     string
	results  []AgentResult
	requests []AgentRequest
}

type blockingAgent struct {
	name    string
	started chan struct{}
}

func (a *blockingAgent) Name() string { return a.name }
func (a *blockingAgent) Run(ctx context.Context, _ AgentRequest) (AgentResult, error) {
	close(a.started)
	<-ctx.Done()
	return AgentResult{}, ctx.Err()
}

func (a *sequenceAgent) Name() string { return a.name }
func (a *sequenceAgent) Run(_ context.Context, request AgentRequest) (AgentResult, error) {
	a.requests = append(a.requests, request)
	result := a.results[0]
	a.results = a.results[1:]
	return result, nil
}

func TestServiceRepairsFailedPatch(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	data.AddRepository(repo)
	data.SetIndex(repo, nil, nil)
	task := domain.Task{ID: "task-1", RepositoryID: repo.ID, Title: "repair", Description: "repair patch", Status: domain.TaskCreated, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	data.AddTask(task)

	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "initial"}, {Output: "repair"}}}
	codebase := &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}
	patch := &sequenceAgent{name: "patch", results: []AgentResult{
		{Output: map[string]any{"proposal": "bad patch"}},
		{Output: map[string]any{"proposal": "fixed patch"}},
	}}
	testAgent := &sequenceAgent{name: "test", results: []AgentResult{
		{Output: sandbox.Report{Status: "apply_failed", Error: "corrupt patch"}},
		{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}},
	}}
	reviewer := &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: map[string]any{"decision": ReviewApprove, "review": "approved"}}}}
	service := &Service{store: data, indexer: indexer.New(), queue: make(chan string, 1), planner: planner, codebase: codebase, patch: patch, test: testAgent, reviewer: reviewer}
	service.execute(context.Background(), task.ID)

	got, err := data.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskCompleted {
		t.Fatalf("status=%s error=%s", got.Status, got.Error)
	}
	steps, err := data.Steps(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"planner", "codebase", "patch", "test", "planner", "patch", "test", "reviewer"}
	if len(steps) != len(want) {
		t.Fatalf("steps=%d want=%d", len(steps), len(want))
	}
	for i, name := range want {
		if steps[i].AgentName != name {
			t.Fatalf("step %d agent=%s want=%s", i, steps[i].AgentName, name)
		}
	}
	if len(patch.requests) != 2 || patch.requests[1].Attempt != 2 {
		t.Fatalf("patch requests=%+v", patch.requests)
	}
	if _, ok := patch.requests[1].Context["repair_feedback"]; !ok {
		t.Fatal("repair feedback was not passed to second patch attempt")
	}
	feedback, ok := patch.requests[1].Context["repair_feedback"].(map[string]any)
	if !ok || feedback["error_kind"] != "apply_failed" {
		t.Fatalf("repair feedback=%+v", patch.requests[1].Context["repair_feedback"])
	}
	if _, ok := patch.requests[1].Context["patch"]; ok {
		t.Fatal("previous patch was duplicated in repair context")
	}
	if _, ok := patch.requests[1].Context["previous_patch"]; ok {
		t.Fatal("previous patch should not anchor a repair attempt")
	}
	if _, ok := patch.requests[1].Context["repair_instruction"]; !ok {
		t.Fatal("repair instruction was not passed to second patch attempt")
	}
	if len(reviewer.requests) != 1 {
		t.Fatal("reviewer was not called")
	}
	history, ok := reviewer.requests[0].Context["attempt_history"].([]map[string]any)
	if !ok || len(history) != 2 {
		t.Fatalf("attempt history=%+v", reviewer.requests[0].Context["attempt_history"])
	}
}

func TestServiceRepairsReviewerRejection(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	data.AddRepository(repo)
	data.SetIndex(repo, nil, nil)
	task := domain.Task{ID: "task-1", RepositoryID: repo.ID, Title: "review repair", Description: "repair review findings", Status: domain.TaskCreated, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	data.AddTask(task)
	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "initial"}, {Output: "review repair"}}}
	codebase := &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}
	patch := &sequenceAgent{name: "patch", results: []AgentResult{{Output: map[string]any{"proposal": "first"}}, {Output: map[string]any{"proposal": "second"}}}}
	testAgent := &sequenceAgent{name: "test", results: []AgentResult{{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}}, {Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}}}}
	reviewer := &sequenceAgent{name: "reviewer", results: []AgentResult{
		{Output: map[string]any{"decision": ReviewRequestChanges, "review": "add focused tests"}},
		{Output: map[string]any{"decision": ReviewApprove, "review": "approved"}},
	}}
	service := &Service{store: data, indexer: indexer.New(), queue: make(chan string, 1), planner: planner, codebase: codebase, patch: patch, test: testAgent, reviewer: reviewer}
	service.execute(context.Background(), task.ID)
	got, err := data.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskCompleted {
		t.Fatalf("status=%s", got.Status)
	}
	if len(patch.requests) != 2 {
		t.Fatalf("patch attempts=%d", len(patch.requests))
	}
	feedback, ok := patch.requests[1].Context["repair_feedback"].(map[string]any)
	if !ok || feedback["source"] != "reviewer" {
		t.Fatalf("feedback=%+v", patch.requests[1].Context["repair_feedback"])
	}
	steps, err := data.Steps(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 9 {
		t.Fatalf("steps=%d", len(steps))
	}
}

func TestTruncateFeedback(t *testing.T) {
	got := truncateFeedback(strings.Repeat("x", maxRepairFeedbackBytes+1))
	if len(got) <= maxRepairFeedbackBytes || !strings.Contains(got, "TRUNCATED") {
		t.Fatalf("feedback length=%d", len(got))
	}
}

func TestCancelTaskPreservesCancelledStatus(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-cancel", RepositoryID: repo.ID, Status: domain.TaskCreated, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	service := NewService(data, indexer.New())
	service.cancelTasks[task.ID] = func() {}
	if err := service.CancelTask(task.ID); err != nil {
		t.Fatal(err)
	}
	got, err := data.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskCancelled {
		t.Fatalf("status=%s", got.Status)
	}
}

func TestResolveHumanReviewApprovesTask(t *testing.T) {
	data := store.NewMemory()
	task := domain.Task{ID: "task-review", RepositoryID: "repo-1", Status: domain.TaskHumanReview, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	service := &Service{store: data}
	got, err := service.ResolveHumanReview(task.ID, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskCompleted {
		t.Fatalf("status=%s", got.Status)
	}
	artifacts, err := data.Artifacts(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Type != "human_review" {
		t.Fatalf("artifacts=%+v", artifacts)
	}
}

func TestResolveHumanReviewRejectsTask(t *testing.T) {
	data := store.NewMemory()
	task := domain.Task{ID: "task-review", RepositoryID: "repo-1", Status: domain.TaskHumanReview, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	service := &Service{store: data}
	got, err := service.ResolveHumanReview(task.ID, false, "rejected because patch is unsafe")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskFailed || !strings.Contains(got.Error, "unsafe") {
		t.Fatalf("task=%+v", got)
	}
}

func TestCancelRunningTaskCancelsAgentContext(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	if err := data.SetIndex(repo, nil, nil); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-running", RepositoryID: repo.ID, Status: domain.TaskCreated, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	planner := &blockingAgent{name: "planner", started: make(chan struct{})}
	service := &Service{store: data, indexer: indexer.New(), queue: make(chan string, 1), planner: planner, workers: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)
	service.enqueue(task.ID)
	select {
	case <-planner.started:
	case <-time.After(time.Second):
		t.Fatal("planner did not start")
	}
	if err := service.CancelTask(task.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		got, err := data.Task(task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == domain.TaskCancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status=%s", got.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartRecoversCreatedTask(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	_ = data.AddRepository(repo)
	_ = data.SetIndex(repo, nil, nil)
	task := domain.Task{ID: "task-recover", RepositoryID: repo.ID, Status: domain.TaskCreated, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = data.AddTask(task)
	service := &Service{store: data, indexer: indexer.New(), queue: make(chan string, 1), workers: 1,
		planner:  &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}},
		codebase: &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}},
		patch:    &sequenceAgent{name: "patch", results: []AgentResult{{Output: map[string]any{"proposal": "patch"}}}},
		test:     &sequenceAgent{name: "test", results: []AgentResult{{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}}}},
		reviewer: &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: map[string]any{"decision": ReviewApprove}}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := data.Task(task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == domain.TaskCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status=%s", got.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartClosesInterruptedRunBeforeRecovery(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	_ = data.AddRepository(repo)
	_ = data.SetIndex(repo, nil, nil)
	task := domain.Task{ID: "task-interrupted", RepositoryID: repo.ID, Status: domain.TaskPlanning, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = data.AddTask(task)
	_ = data.AddRun(domain.TaskRun{ID: "run-old", TaskID: task.ID, Status: domain.TaskPlanning, StartedAt: time.Now()})
	service := &Service{store: data, indexer: indexer.New(), queue: make(chan string, 1), workers: 1,
		planner:  &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}},
		codebase: &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}},
		patch:    &sequenceAgent{name: "patch", results: []AgentResult{{Output: map[string]any{"proposal": "patch"}}}},
		test:     &sequenceAgent{name: "test", results: []AgentResult{{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}}}},
		reviewer: &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: map[string]any{"decision": ReviewApprove}}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, _ := data.Task(task.ID)
		if got.Status == domain.TaskCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status=%s", got.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	runs, err := data.Runs(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs=%d", len(runs))
	}
	if runs[0].Status != domain.TaskFailed || runs[0].EndedAt.IsZero() {
		t.Fatalf("old run=%+v", runs[0])
	}
}

func TestWorkerCountConfiguration(t *testing.T) {
	t.Setenv("CODECODRIVER_WORKERS", "3")
	if got := workerCount(); got != 3 {
		t.Fatalf("workers=%d", got)
	}
	t.Setenv("CODECODRIVER_WORKERS", "99")
	if got := workerCount(); got != 1 {
		t.Fatalf("workers=%d", got)
	}
}
