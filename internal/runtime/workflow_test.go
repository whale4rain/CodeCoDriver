package runtime

import (
	"context"
	"os"
	"path/filepath"
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

func TestRunPatchLoopUsesWorkspaceAcrossRepairs(t *testing.T) {
	source := t.TempDir()
	original := "package sample\n\nfunc Value() int { return 1 }\n"
	if err := os.WriteFile(filepath.Join(source, "sample.go"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	data, repo, task := workflowTestFixture(t, "dynamic-engineering")
	repo.Path = source
	if err := data.AddRun(domain.TaskRun{ID: "run-1", TaskID: task.ID, Status: domain.TaskIndexCheck, FencingToken: 1, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	proposal := map[string]any{"proposal": "--- a/sample.go\n+++ b/sample.go\n@@ -1,3 +1,3 @@\n package sample\n \n-func Value() int { return 1 }\n+func Value() int { return 2 }\n"}
	patch := &sequenceAgent{name: "patch", results: []AgentResult{{Output: proposal}, {Output: proposal}}}
	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}}
	testAgent := &sequenceAgent{name: "test", results: []AgentResult{
		{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}},
		{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}},
	}}
	reviewer := &sequenceAgent{name: "reviewer", results: []AgentResult{
		{Output: map[string]any{"decision": ReviewRequestChanges, "review": "fix the test"}},
		{Output: map[string]any{"decision": ReviewApprove, "review": "approved"}},
	}}

	var created *fakeWorkspace
	service := &Service{
		store:    data,
		indexer:  indexer.New(),
		planner:  planner,
		patch:    patch,
		test:     testAgent,
		reviewer: reviewer,
		workspaceFactory: func(_ context.Context, sourcePath string) (sandbox.Workspace, error) {
			if sourcePath != source {
				t.Fatalf("workspace factory received source %q", sourcePath)
			}
			created = newFakeWorkspace(t, source)
			return created, nil
		},
	}

	decision, err := service.runPatchLoop(context.Background(), task, repo, "run-1", 1, map[string]any{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if decision != ReviewApprove {
		t.Fatalf("decision=%s", decision)
	}
	if created == nil {
		t.Fatal("workspace factory was not called")
	}
	if created.resetCalls != 1 {
		t.Fatalf("workspace reset calls=%d, want 1", created.resetCalls)
	}
	if created.closeCalls != 1 {
		t.Fatalf("workspace close calls=%d, want 1", created.closeCalls)
	}
	if len(patch.requests) != 2 || len(testAgent.requests) != 2 || len(reviewer.requests) != 2 {
		t.Fatalf("patch/test/reviewer calls=%d/%d/%d", len(patch.requests), len(testAgent.requests), len(reviewer.requests))
	}
	for _, request := range append(append(append([]AgentRequest{}, patch.requests...), testAgent.requests...), reviewer.requests...) {
		if request.Workspace == nil || request.Workspace != created {
			t.Fatal("agent request did not receive the isolated workspace")
		}
	}
	raw, err := os.ReadFile(filepath.Join(source, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Fatal("host repository was modified")
	}
}

func TestExecuteWorkflowCreatesWorkspaceBeforeCodebase(t *testing.T) {
	source := t.TempDir()
	original := "package sample\n\nfunc Value() int { return 1 }\n"
	if err := os.WriteFile(filepath.Join(source, "sample.go"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	data, repo, task := workflowTestFixture(t, "dynamic-engineering")
	repo.Path = source
	task.MemoryMode = domain.MemoryModeWithout
	if err := data.AddRun(domain.TaskRun{ID: "run-1", TaskID: task.ID, Status: domain.TaskIndexCheck, FencingToken: 1, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}}
	codebase := &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}
	patch := &sequenceAgent{name: "patch", results: []AgentResult{{Output: map[string]any{"proposal": "--- a/sample.go\n+++ b/sample.go\n@@ -1,3 +1,3 @@\n package sample\n \n-func Value() int { return 1 }\n+func Value() int { return 2 }\n"}}}}
	testAgent := &sequenceAgent{name: "test", results: []AgentResult{{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}}}}
	reviewer := &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: map[string]any{"decision": ReviewApprove, "review": "approved"}}}}

	var created *fakeWorkspace
	service := workflowTestService(data, planner, codebase, &sequenceAgent{name: "explainer"}, &sequenceAgent{name: "orchestrator"}, patch, testAgent, reviewer)
	service.workspaceFactory = func(_ context.Context, sourcePath string) (sandbox.Workspace, error) {
		if sourcePath != source {
			t.Fatalf("workspace factory received source %q", sourcePath)
		}
		created = newFakeWorkspace(t, source)
		return created, nil
	}

	var runErr error
	service.executeWorkflow(
		context.Background(),
		task,
		repo,
		"run-1",
		1,
		WorkflowStandardAgentLoop,
		map[string]any{},
		nil,
		func(err error) { runErr = err },
		func(domain.TaskStatus, string) error { return nil },
	)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if created == nil {
		t.Fatal("workspace factory was not called")
	}
	if len(codebase.requests) != 1 {
		t.Fatalf("codebase requests=%d, want 1", len(codebase.requests))
	}
	if len(patch.requests) != 1 || len(testAgent.requests) != 1 || len(reviewer.requests) != 1 {
		t.Fatalf("patch/test/reviewer requests=%d/%d/%d", len(patch.requests), len(testAgent.requests), len(reviewer.requests))
	}
	for _, request := range append(append(append([]AgentRequest{codebase.requests[0]}, patch.requests...), testAgent.requests...), reviewer.requests...) {
		if request.Workspace == nil || request.Workspace != created {
			t.Fatal("agent request did not receive the isolated workspace")
		}
	}
	if created.closeCalls != 1 {
		t.Fatalf("workspace close calls=%d, want 1", created.closeCalls)
	}
	raw, err := os.ReadFile(filepath.Join(source, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Fatal("host repository was modified")
	}
}
