package runtime

import (
	"strings"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/store"
)

func TestEvaluationAutoApprovesHumanReview(t *testing.T) {
	data := store.NewMemory()
	task := domain.Task{ID: "task-eval-human", RepositoryID: "repo-1", Status: domain.TaskHumanReview, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	if err := data.AddRun(domain.TaskRun{ID: "run-eval-human", TaskID: task.ID, Status: "completed", StartedAt: time.Now(), EndedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := data.AddArtifact(domain.Artifact{ID: "test-eval-human", TaskID: task.ID, RunID: "run-eval-human", Type: "test_report", Name: "attempt-1-sandbox-report.json", Content: `{"status":"passed","applied":true,"passed":true}`, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	run := domain.EvaluationRun{ID: "run-eval-human", CaseID: "case-1", TaskID: task.ID, Mode: "agent", Status: "human_review_required", StartedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddEvaluationRun(run); err != nil {
		t.Fatal(err)
	}
	service := &Service{store: data, queue: make(chan string, 1)}
	service.maybeAutoHandleEvaluationHumanReview(task)
	got, err := data.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskCompleted {
		t.Fatalf("status=%s", got.Status)
	}
	runs, err := data.AllEvaluationRuns()
	if err != nil || !strings.Contains(runs[0].Notes, "auto_approved") {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}

func TestEvaluationAutoRejectsHumanReviewWithoutRealSandboxPass(t *testing.T) {
	data := store.NewMemory()
	task := domain.Task{ID: "task-eval-reject", RepositoryID: "repo-1", Status: domain.TaskHumanReview, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	if err := data.AddRun(domain.TaskRun{ID: "run-eval-reject", TaskID: task.ID, Status: "completed", StartedAt: time.Now(), EndedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := data.AddArtifact(domain.Artifact{ID: "test-eval-reject", TaskID: task.ID, RunID: "run-eval-reject", Type: "test_report", Name: "attempt-3-sandbox-report.json", Content: `{"status":"apply_failed","applied":false,"passed":false}`, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	run := domain.EvaluationRun{ID: "run-eval-reject", CaseID: "case-1", TaskID: task.ID, Mode: "agent", Status: "human_review_required", StartedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddEvaluationRun(run); err != nil {
		t.Fatal(err)
	}
	service := &Service{store: data, queue: make(chan string, 1)}
	service.maybeAutoHandleEvaluationHumanReview(task)
	got, err := data.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskFailed {
		t.Fatalf("status=%s", got.Status)
	}
	runs, err := data.AllEvaluationRuns()
	if err != nil || runs[0].Passed {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	if err != nil || !strings.Contains(runs[0].Notes, "auto_rejected_no_sandbox") {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}

func TestEvaluationAutoFeedbackFromReviewRequirement(t *testing.T) {
	data := store.NewMemory()
	task := domain.Task{ID: "task-eval-feedback", RepositoryID: "repo-1", Status: domain.TaskHumanReview, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	run := domain.EvaluationRun{ID: "run-eval-feedback", CaseID: "case-1", TaskID: task.ID, Mode: "agent", Status: "human_review_required", StartedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddEvaluationRun(run); err != nil {
		t.Fatal(err)
	}
	if err := data.AddArtifact(domain.Artifact{ID: "review-eval", TaskID: task.ID, Type: "review", Content: "Required: Re-run validation with `go test ./internal/auth/` and confirm tests pass.", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	service := &Service{store: data, queue: make(chan string, 1)}
	service.maybeAutoHandleEvaluationHumanReview(task)
	got, err := data.Task(task.ID)
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
		t.Fatal("auto feedback did not enqueue task")
	}
	artifacts, err := data.Artifacts(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasArtifactType(artifacts, "human_feedback") {
		t.Fatalf("human feedback artifact missing: %+v", artifacts)
	}
	runs, err := data.AllEvaluationRuns()
	if err != nil || !strings.Contains(runs[0].Notes, "auto_feedback") {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}

func TestEvaluationAutoFeedbackOnPlannerSkip(t *testing.T) {
	data := store.NewMemory()
	task := domain.Task{ID: "task-eval-skip", RepositoryID: "repo-1", Status: domain.TaskHumanReview, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	run := domain.EvaluationRun{ID: "run-eval-skip", CaseID: "case-1", TaskID: task.ID, Mode: "agent", Status: "human_review_required", StartedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddEvaluationRun(run); err != nil {
		t.Fatal(err)
	}
	if err := data.AddArtifact(domain.Artifact{ID: "skip-eval", TaskID: task.ID, Type: "planner_skip", Content: `{"decision":"SKIP_SUGGESTED"}`, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	service := &Service{store: data, queue: make(chan string, 1)}
	service.maybeAutoHandleEvaluationHumanReview(task)
	got, err := data.Task(task.ID)
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
		t.Fatal("skip override did not enqueue task")
	}
}

func TestEvaluationFeedbackFromReviewStopsAtMarker(t *testing.T) {
	data := store.NewMemory()
	task := domain.Task{ID: "task-eval-marker", RepositoryID: "repo-1", Status: domain.TaskHumanReview, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	if err := data.AddArtifact(domain.Artifact{ID: "review-marker", TaskID: task.ID, Type: "review", Content: "Required: Add focused tests. ### Regression Risk\nMore text", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	service := &Service{store: data}
	feedback := service.evaluationFeedbackFromReview(task.ID)
	if !strings.Contains(feedback, "Add focused tests") || strings.Contains(feedback, "###") || strings.Contains(feedback, "Regression") {
		t.Fatalf("feedback=%q", feedback)
	}
}
