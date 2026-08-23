package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/sandbox"
	"codecodriver/internal/skills"
	"codecodriver/internal/store"
)

func TestWorkflowSpecSelection(t *testing.T) {
	service := &Service{}
	if got := service.workflowSpecFor(WorkflowStandardAgentLoop).Initial; got != "codebase" {
		t.Fatalf("standard initial=%s", got)
	}
	if got := service.workflowSpecFor(WorkflowDocumentationLoop).Initial; got != "codebase" {
		t.Fatalf("documentation initial=%s", got)
	}
	if got := service.workflowSpecFor(WorkflowExplanationAgentLoop).Initial; got != "codebase" {
		t.Fatalf("explanation initial=%s", got)
	}
	if got := service.workflowSpecFor(WorkflowDynamicAgentLoop).Initial; got != "orchestrator" {
		t.Fatalf("dynamic initial=%s", got)
	}
}

func TestParseWorkflowDecision(t *testing.T) {
	output := map[string]any{
		"decision":  "explain",
		"next_step": "codebase",
		"target":    "explainer",
		"reason":    "read-only task",
	}
	got := parseWorkflowDecision(output)
	if got.Decision != "explain" || got.Next != "codebase" || got.Target != "explainer" {
		t.Fatalf("map decision=%+v", got)
	}
	text := "```json\n{\"decision\":\"request_human\",\"next_step\":\"finish\",\"reason\":\"ambiguous\"}\n```"
	got = parseWorkflowDecision(text)
	if got.Decision != "request_human" || got.Next != "finish" {
		t.Fatalf("text decision=%+v", got)
	}
	if got := parseWorkflowDecision("not json"); got.Next != "" {
		t.Fatalf("invalid decision=%+v", got)
	}
}

func TestExecuteDynamicWorkflowRoutesToCodeChange(t *testing.T) {
	data, _, task := workflowTestFixture(t, "dynamic-engineering")
	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}}
	orchestrator := &sequenceAgent{name: "orchestrator", results: []AgentResult{{Output: WorkflowDecision{Decision: "code_change", Next: "codebase", Target: "patch_loop", Reason: "engineering task"}}}}
	codebase := &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}
	patch := &sequenceAgent{name: "patch", results: []AgentResult{{Output: map[string]any{"proposal": "--- a/a.go\n+++ b/a.go\n"}}}}
	testAgent := &sequenceAgent{name: "test", results: []AgentResult{{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}}}}
	reviewer := &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: map[string]any{"decision": ReviewApprove}}}}
	service := workflowTestService(data, planner, codebase, &sequenceAgent{name: "explainer"}, orchestrator, patch, testAgent, reviewer)
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
	names := []string{}
	for _, step := range steps {
		names = append(names, step.AgentName)
	}
	if strings.Join(names, ",") != "planner,orchestrator,codebase,patch,test,reviewer" {
		t.Fatalf("steps=%v", names)
	}
	if len(reviewer.requests) != 1 {
		t.Fatalf("reviewer requests=%d", len(reviewer.requests))
	}
}

func TestExecuteDynamicWorkflowRoutesToExplainer(t *testing.T) {
	data, _, task := workflowTestFixture(t, "dynamic-engineering")
	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}}
	orchestrator := &sequenceAgent{name: "orchestrator", results: []AgentResult{{Output: WorkflowDecision{Decision: "explain", Next: "codebase", Target: "explainer", Reason: "read-only explanation"}}}}
	codebase := &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}
	explainer := &sequenceAgent{name: "explainer", results: []AgentResult{{Output: map[string]any{"explanation": "explained"}, ArtifactType: "explanation", ArtifactName: "explanation.md", ArtifactContent: "# Explanation"}}}
	patch := &sequenceAgent{name: "patch", results: []AgentResult{{Output: "patch"}}}
	testAgent := &sequenceAgent{name: "test", results: []AgentResult{{Output: "test"}}}
	reviewer := &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: "review"}}}
	service := workflowTestService(data, planner, codebase, explainer, orchestrator, patch, testAgent, reviewer)
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
	names := []string{}
	for _, step := range steps {
		names = append(names, step.AgentName)
	}
	if strings.Join(names, ",") != "planner,orchestrator,codebase,explainer" {
		t.Fatalf("steps=%v", names)
	}
	if len(patch.requests) != 0 || len(testAgent.requests) != 0 || len(reviewer.requests) != 0 {
		t.Fatal("dynamic explain workflow should not run patch/test/reviewer")
	}
}

func TestExecuteDynamicWorkflowRequestsHuman(t *testing.T) {
	data, _, task := workflowTestFixture(t, "dynamic-engineering")
	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}}
	orchestrator := &sequenceAgent{name: "orchestrator", results: []AgentResult{{Output: WorkflowDecision{Decision: "request_human", Next: "finish", Reason: "ambiguous task"}}}}
	codebase := &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}
	patch := &sequenceAgent{name: "patch", results: []AgentResult{{Output: "patch"}}}
	testAgent := &sequenceAgent{name: "test", results: []AgentResult{{Output: "test"}}}
	reviewer := &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: "review"}}}
	service := workflowTestService(data, planner, codebase, &sequenceAgent{name: "explainer"}, orchestrator, patch, testAgent, reviewer)
	service.execute(context.Background(), task.ID)

	got, err := data.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskHumanReview {
		t.Fatalf("status=%s", got.Status)
	}
	steps, err := data.Steps(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].AgentName != "planner" || steps[1].AgentName != "orchestrator" {
		t.Fatalf("steps=%+v", steps)
	}
	if len(codebase.requests) != 0 {
		t.Fatal("codebase should not run after request_human")
	}
}

func TestExecuteDynamicWorkflowFallsBackWithoutOrchestrator(t *testing.T) {
	data, _, task := workflowTestFixture(t, "dynamic-engineering")
	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}}
	codebase := &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}
	patch := &sequenceAgent{name: "patch", results: []AgentResult{{Output: map[string]any{"proposal": "--- a/a.go\n+++ b/a.go\n"}}}}
	testAgent := &sequenceAgent{name: "test", results: []AgentResult{{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}}}}
	reviewer := &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: map[string]any{"decision": ReviewApprove}}}}
	service := workflowTestService(data, planner, codebase, &sequenceAgent{name: "explainer"}, nil, patch, testAgent, reviewer)
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
	for _, step := range steps {
		if step.AgentName == "orchestrator" {
			t.Fatal("orchestrator fallback should not create a trace step")
		}
	}
}

func workflowTestFixture(t *testing.T, skillName string) (*store.Memory, domain.Repository, domain.Task) {
	t.Helper()
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-workflow", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-workflow", RepositoryID: repo.ID, Title: "dynamic task", Description: "dynamic task", SkillName: skillName, Status: domain.TaskCreated, MemoryMode: domain.MemoryModeWith, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	return data, repo, task
}

func workflowTestService(data *store.Memory, planner, codebase, explainer, orchestrator, patch, testAgent, reviewer Agent) *Service {
	registry := skills.New()
	_ = registry.Register(skills.Skill{Name: "dynamic-engineering", Workflow: WorkflowDynamicAgentLoop})
	return &Service{
		store: data, indexer: indexer.New(), queue: make(chan string, 1),
		planner: planner, codebase: codebase, explainer: explainer, orchestrator: orchestrator,
		patch: patch, test: testAgent, reviewer: reviewer,
		skillRegistry: registry, taskRouter: skills.NewRouter(registry),
	}
}
