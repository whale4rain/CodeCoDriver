package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/skills"
	"codecodriver/internal/store"
)

func TestExplainAgentProducesExplanationArtifact(t *testing.T) {
	fake := &recordingLLM{responses: []string{"# Pagination Flow\n\n..."}}
	request := AgentRequest{
		Task:       domain.Task{Title: "Explain pagination", Description: "Explain how pagination works"},
		Repository: domain.Repository{Name: "sample"},
		Context: map[string]any{
			"skills": []skills.Skill{{
				Name:     "code-explainer",
				Workflow: "explanation_agent_loop",
				Prompts: map[string]skills.PromptTemplate{
					"explainer": {User: "EXPLANATION OUTPUT: Explain {{task_title}} with evidence."},
				},
			}},
			"codebase": map[string]any{
				"context_pack": "FILE: pkg/pagination/pages.go\nfunc New() {}",
			},
		},
	}
	result, err := (ExplainAgent{LLM: fake}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.prompts[0], "EXPLANATION OUTPUT") || !strings.Contains(fake.prompts[0], "RETRIEVED CONTEXT") {
		t.Fatalf("prompt missing explanation context: %s", fake.prompts[0])
	}
	if result.ArtifactType != "explanation" || !strings.Contains(result.ArtifactContent, "Pagination Flow") {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecuteExplanationWorkflowCompletesWithoutPatch(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-explain", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-explain", RepositoryID: repo.ID, Title: "Explain pagination", Description: "Explain how pagination works", SkillName: "code-explainer", Status: domain.TaskCreated, MemoryMode: domain.MemoryModeWith, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}}
	codebase := &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}
	explainer := &sequenceAgent{name: "explainer", results: []AgentResult{{Output: map[string]any{"explanation": "# Pagination Flow"}, ArtifactType: "explanation", ArtifactName: "explanation.md", ArtifactContent: "# Pagination Flow"}}}
	patch := &sequenceAgent{name: "patch", results: []AgentResult{{Output: "patch"}}}
	testAgent := &sequenceAgent{name: "test", results: []AgentResult{{Output: "test"}}}
	reviewer := &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: "review"}}}
	registry := skills.DefaultRegistry()
	service := &Service{store: data, indexer: indexer.New(), queue: make(chan string, 1), planner: planner, codebase: codebase, explainer: explainer, patch: patch, test: testAgent, reviewer: reviewer, skillRegistry: registry, taskRouter: skills.NewRouter(registry)}
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
	if strings.Join(names, ",") != "planner,codebase,explainer" {
		t.Fatalf("steps=%v", names)
	}
	if len(patch.requests) != 0 || len(testAgent.requests) != 0 || len(reviewer.requests) != 0 {
		t.Fatal("explanation workflow should not run patch/test/reviewer")
	}
	artifacts, err := data.Artifacts(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, artifact := range artifacts {
		if artifact.Type == "explanation" {
			found = true
		}
	}
	if !found {
		t.Fatalf("explanation artifact missing: %+v", artifacts)
	}
}
