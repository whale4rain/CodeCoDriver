package runtime

import (
	"context"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/store"
)

func TestPlannerSuggestsSkipForExistingDocumentationDeliverable(t *testing.T) {
	task := domain.Task{
		ID:          "task-doc-2",
		Title:       "生成中文README文档",
		Description: "创建或补充中文 README_zh.md 文档",
		MemoryMode:  domain.MemoryModeWith,
	}
	memory := domain.MemoryEntry{
		ID:           "memory-doc-1",
		TaskID:       "task-doc-1",
		Title:        "生成中文README",
		Summary:      "Applied task patch to repository. Files: README_zh.md",
		Content:      "Applied task patch to repository. Files: README_zh.md",
		Kind:         "execution_success",
		ChangedFiles: []string{"README_zh.md"},
		SuccessScore: 1,
		Source:       "applier",
	}
	request := AgentRequest{
		Task: task,
		Files: []domain.RepositoryFile{{
			Path:     "README_zh.md",
			Language: "markdown",
		}},
		Context: map[string]any{
			"memory_candidates": []domain.MemoryEntry{memory},
			"memory":            []domain.MemoryEntry{memory},
		},
	}
	result, err := (PlannerAgent{}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	output, ok := result.Output.(map[string]any)
	if !ok {
		t.Fatalf("output type=%T", result.Output)
	}
	if output["decision"] != PlannerSkipDecision {
		t.Fatalf("decision=%v want=%s", output["decision"], PlannerSkipDecision)
	}
	evidence, ok := output["evidence"].(plannerSkipEvidence)
	if !ok {
		t.Fatalf("evidence type=%T", output["evidence"])
	}
	if evidence.MemoryID != memory.ID || evidence.Score < minPlannerSkipEvidenceScore {
		t.Fatalf("evidence=%+v", evidence)
	}
	if result.ArtifactType != plannerSkipArtifactType {
		t.Fatalf("artifact type=%s", result.ArtifactType)
	}
	if !hasPlannerArtifactDecision([]domain.Artifact{{Type: result.ArtifactType, Content: result.ArtifactContent}}, plannerSkipDecision) {
		t.Fatalf("planner artifact does not expose skip decision: %s", result.ArtifactContent)
	}
}

func TestPlannerDoesNotSuggestSkipAfterUserContinue(t *testing.T) {
	memory := domain.MemoryEntry{
		ID:           "memory-doc-1",
		Title:        "生成中文README",
		Summary:      "Applied task patch to repository. Files: README_zh.md",
		Content:      "Applied task patch to repository. Files: README_zh.md",
		Kind:         "execution_success",
		ChangedFiles: []string{"README_zh.md"},
		SuccessScore: 1,
	}
	request := AgentRequest{
		Task: domain.Task{
			Title:       "生成中文README文档",
			Description: "创建或补充中文 README_zh.md 文档",
		},
		Files: []domain.RepositoryFile{{Path: "README_zh.md"}},
		Artifacts: []domain.Artifact{{
			Type:    plannerSkipArtifactType,
			Content: `{"decision":"continue","reason":"still needed"}`,
		}},
		Context: map[string]any{"memory_candidates": []domain.MemoryEntry{memory}},
	}
	result, err := (PlannerAgent{}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	output, ok := result.Output.(map[string]any)
	if !ok {
		t.Fatalf("output type=%T", result.Output)
	}
	if output["decision"] == PlannerSkipDecision {
		t.Fatalf("planner repeated skip suggestion after user chose continue: %v", output)
	}
}

func TestExecutePausesAtPlannerSkipSuggestion(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-skip", RepositoryID: repo.ID, Title: "生成中文README文档", Description: "创建或补充中文 README_zh.md 文档", Status: domain.TaskCreated, MemoryMode: domain.MemoryModeWith, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: map[string]any{"decision": PlannerSkipDecision, "reason": "already done"}, ArtifactType: plannerSkipArtifactType, ArtifactName: "planner-skip.json", ArtifactContent: `{"decision":"SKIP_SUGGESTED"}`}}}
	codebase := &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}
	service := &Service{store: data, indexer: indexer.New(), queue: make(chan string, 1), planner: planner, codebase: codebase}
	service.execute(context.Background(), task.ID)

	got, err := data.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskHumanReview {
		t.Fatalf("status=%s want=%s", got.Status, domain.TaskHumanReview)
	}
	runs, err := data.Runs(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].EndedAt.IsZero() {
		t.Fatalf("runs=%+v", runs)
	}
	steps, err := data.Steps(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].AgentName != "planner" {
		t.Fatalf("steps=%+v", steps)
	}
	if len(codebase.requests) != 0 {
		t.Fatalf("codebase should not run after skip suggestion")
	}
	artifacts, err := data.Artifacts(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, artifact := range artifacts {
		if artifact.Type == plannerSkipArtifactType {
			found = true
		}
	}
	if !found {
		t.Fatalf("planner skip artifact missing: %+v", artifacts)
	}
}

func TestExecuteUsesPlannerSkipProposalFromMemory(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	files := []domain.RepositoryFile{{RepositoryID: repo.ID, Path: "README_zh.md", Language: "markdown", Size: 1, Hash: "readme"}}
	if err := data.SetIndex(repo, files, nil); err != nil {
		t.Fatal(err)
	}
	memory := domain.MemoryEntry{
		ID:           "memory-e2e",
		RepositoryID: repo.ID,
		TaskID:       "task-doc-1",
		Kind:         "execution_success",
		Title:        "生成中文README",
		Summary:      "Applied task patch to repository. Files: README_zh.md",
		Content:      "Applied task patch to repository. Files: README_zh.md",
		ChangedFiles: []string{"README_zh.md"},
		SuccessScore: 1,
		Source:       "applier",
		CreatedAt:    time.Now(),
	}
	if err := data.AddMemory(memory); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-skip-e2e", RepositoryID: repo.ID, Title: "生成中文README文档", Description: "创建或补充中文 README_zh.md 文档", Status: domain.TaskCreated, MemoryMode: domain.MemoryModeWith, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	service := NewService(data, indexer.New())
	service.execute(context.Background(), task.ID)

	got, err := data.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskHumanReview {
		t.Fatalf("status=%s want=%s error=%s", got.Status, domain.TaskHumanReview, got.Error)
	}
}

func TestResolveHumanReviewContinueRunsSkippedTask(t *testing.T) {
	data := store.NewMemory()
	task := domain.Task{ID: "task-skip-2", RepositoryID: "repo-1", Status: domain.TaskHumanReview, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	run := domain.TaskRun{ID: "run-skip-2", TaskID: task.ID, Status: domain.TaskHumanReview, StartedAt: time.Now()}
	if err := data.AddRun(run); err != nil {
		t.Fatal(err)
	}
	if err := data.AddArtifact(domain.Artifact{ID: "artifact-skip-2", TaskID: task.ID, RunID: run.ID, Type: plannerSkipArtifactType, Content: `{"decision":"SKIP_SUGGESTED"}`, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	service := &Service{store: data, queue: make(chan string, 1)}
	got, err := service.ResolveHumanReview(task.ID, false, "still need this change")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskCreated {
		t.Fatalf("status=%s want=%s", got.Status, domain.TaskCreated)
	}
	select {
	case queued := <-service.queue:
		if queued != task.ID {
			t.Fatalf("queued=%s", queued)
		}
	default:
		t.Fatal("task was not enqueued after user chose to continue")
	}
	artifacts, err := data.Artifacts(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPlannerArtifactDecision(artifacts, plannerContinueDecision) {
		t.Fatalf("continue override missing: %+v", artifacts)
	}
}
