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

func TestExecuteRoutesExplicitSkillAndPersistsSelection(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-skill", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-skill", RepositoryID: repo.ID, Title: "build a readme", Description: "build a readme for this repo", SkillName: "documentation", Status: domain.TaskCreated, MemoryMode: domain.MemoryModeWith, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	planner := &sequenceAgent{name: "planner", results: []AgentResult{{Output: "plan"}}}
	codebase := &sequenceAgent{name: "codebase", results: []AgentResult{{Output: "context"}}}
	patch := &sequenceAgent{name: "patch", results: []AgentResult{{Output: map[string]any{"proposal": "--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old\n+new\n"}}}}
	testAgent := &sequenceAgent{name: "test", results: []AgentResult{{Output: sandbox.Report{Status: "passed", Applied: true, Passed: true}}}}
	reviewer := &sequenceAgent{name: "reviewer", results: []AgentResult{{Output: map[string]any{"decision": ReviewApprove}}}}
	service := &Service{store: data, indexer: indexer.New(), queue: make(chan string, 1), planner: planner, codebase: codebase, patch: patch, test: testAgent, reviewer: reviewer, skillRegistry: skills.DefaultRegistry(), taskRouter: skills.NewRouter(skills.DefaultRegistry())}
	service.execute(context.Background(), task.ID)

	if len(planner.requests) == 0 {
		t.Fatal("planner did not run")
	}
	selected, ok := planner.requests[0].Context["skills"].([]skills.Skill)
	if !ok || len(selected) != 1 || selected[0].Name != "documentation" {
		t.Fatalf("skills=%v ok=%v", planner.requests[0].Context["skills"], ok)
	}
	artifacts, err := data.Artifacts(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, artifact := range artifacts {
		if artifact.Type == "skill_selection" && strings.Contains(artifact.Content, `"primary_skill": "documentation"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("skill selection artifact missing: %+v", artifacts)
	}
}

func TestPlannerUsesRenderedSkillPromptTemplate(t *testing.T) {
	fake := &recordingLLM{responses: []string{"plan"}}
	request := AgentRequest{
		Task:       domain.Task{Title: "fix docs", Description: "fix docs"},
		Repository: domain.Repository{Name: "sample"},
		Context: map[string]any{"skills": []skills.Skill{{
			Name:     "api-review",
			Workflow: "standard_agent_loop",
			Prompts: map[string]skills.PromptTemplate{
				"planner": {User: "Use {{selected_skill}} for {{task_title}} and {{task_description}}."},
			},
		}}},
	}
	if _, err := (PlannerAgent{LLM: fake}).Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	prompt := fake.prompts[0]
	if !strings.Contains(prompt, "SKILL [api-review] RULES") || !strings.Contains(prompt, "Use api-review for fix docs and fix docs") {
		t.Fatalf("skill prompt not rendered: %s", prompt)
	}
}

func TestPlannerFailsOnBrokenSkillTemplate(t *testing.T) {
	fake := &recordingLLM{responses: []string{"plan"}}
	request := AgentRequest{
		Task:       domain.Task{Title: "fix docs", Description: "fix docs"},
		Repository: domain.Repository{Name: "sample"},
		Context: map[string]any{"skills": []skills.Skill{{
			Name:     "broken",
			Workflow: "standard_agent_loop",
			Prompts: map[string]skills.PromptTemplate{
				"planner": {User: "Use {{missing_variable}}."},
			},
		}}},
	}
	if _, err := (PlannerAgent{LLM: fake}).Run(context.Background(), request); err == nil {
		t.Fatal("broken template should fail")
	}
}
