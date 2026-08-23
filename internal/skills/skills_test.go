package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codecodriver/internal/domain"
)

func TestPromptTemplateRenderAndMissingVariables(t *testing.T) {
	template := PromptTemplate{User: "Repo: {{repository_name}}\nTask: {{task_title}}"}
	rendered, err := template.Render(map[string]string{"repository_name": "sample", "task_title": "fix retry"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "sample") || !strings.Contains(rendered, "fix retry") {
		t.Fatalf("rendered=%s", rendered)
	}
	if _, err := template.Render(map[string]string{"repository_name": "sample"}); err == nil {
		t.Fatal("missing task_title should fail")
	}
}

func TestDefaultRegistryLoadsSkills(t *testing.T) {
	registry := DefaultRegistry()
	for _, name := range []string{"documentation", "go-testing", "general", "dynamic-agent"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("missing default skill %s", name)
		}
	}
	skill, ok := registry.Get("dynamic-agent")
	if !ok || skill.Workflow != "dynamic_agent_loop" {
		t.Fatalf("dynamic-agent skill=%+v", skill)
	}
}

func TestTaskRouterSelectsDocumentationByKeywordAndPath(t *testing.T) {
	registry := DefaultRegistry()
	router := NewRouter(registry)
	result, err := router.Route(RouteInput{
		Task: domain.Task{
			Title:       "生成中文readme",
			Description: "build a Chinese README document",
		},
		Repository: domain.Repository{Name: "sample"},
		Files: []domain.RepositoryFile{{
			Path: "README_zh.md",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.PrimarySkill != "documentation" {
		t.Fatalf("skill=%s scores=%v", result.PrimarySkill, result.Scores)
	}
	if result.Workflow != "documentation_agent_loop" {
		t.Fatalf("workflow=%s", result.Workflow)
	}
}

func TestTaskRouterHonorsExplicitSkill(t *testing.T) {
	router := NewRouter(DefaultRegistry())
	result, err := router.Route(RouteInput{
		Task: domain.Task{
			Title:     "fix retry",
			SkillName: "go-testing",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.PrimarySkill != "go-testing" {
		t.Fatalf("skill=%s", result.PrimarySkill)
	}
	if _, err := router.Route(RouteInput{Task: domain.Task{SkillName: "missing"}}); err == nil {
		t.Fatal("unknown skill should fail")
	}
}

func TestTaskRouterFallsBackToGeneral(t *testing.T) {
	router := NewRouter(DefaultRegistry())
	result, err := router.Route(RouteInput{
		Task: domain.Task{Title: "refactor unrelated code", Description: "clean up internals"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.PrimarySkill != "general" {
		t.Fatalf("skill=%s scores=%v", result.PrimarySkill, result.Scores)
	}
}

func TestTaskRouterDoesNotRouteFromMemoryAlone(t *testing.T) {
	router := NewRouter(DefaultRegistry())
	result, err := router.Route(RouteInput{
		Task: domain.Task{Title: "fix retry timeout", Description: "fix retry timeout"},
		Memories: []domain.MemoryEntry{{
			Title:   "build a readme",
			Summary: "documentation",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.PrimarySkill != "general" {
		t.Fatalf("skill=%s scores=%v", result.PrimarySkill, result.Scores)
	}
}

func TestTaskRouterSelectsCodeExplainer(t *testing.T) {
	router := NewRouter(DefaultRegistry())
	result, err := router.Route(RouteInput{
		Task: domain.Task{
			Title:       "Explain pagination flow",
			Description: "Explain how pagination works and which functions own the logic",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.PrimarySkill != "code-explainer" {
		t.Fatalf("skill=%s scores=%v", result.PrimarySkill, result.Scores)
	}
	if result.Workflow != "explanation_agent_loop" {
		t.Fatalf("workflow=%s", result.Workflow)
	}
}

func TestRegistryLoadCustomSkill(t *testing.T) {
	registry := New()
	data := `{"name":"api-review","description":"API contract review","keywords":["api","contract"],"prompts":{"reviewer":{"user":"Review {{task_title}} as an API contract."}}}`
	if err := registry.Load([]byte(data)); err != nil {
		t.Fatal(err)
	}
	skill, ok := registry.Get("api-review")
	if !ok {
		t.Fatal("custom skill not registered")
	}
	if _, ok := skill.Prompt("reviewer"); !ok {
		t.Fatal("reviewer prompt missing")
	}
}

func TestRegistryLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills.json")
	data := `{"name":"file-skill","description":"loaded from file","keywords":["file"],"prompts":{"planner":{"user":"Plan {{task_title}} from file."}}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := New()
	if err := registry.LoadFile(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("file-skill"); !ok {
		t.Fatal("file skill not loaded")
	}
}
