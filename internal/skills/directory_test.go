package skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryLoadDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.json"), []byte(`{"name":"one","description":"one","prompts":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.json"), []byte(`{"name":"two","description":"two","prompts":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := New()
	if err := registry.LoadDirectory(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("one"); !ok {
		t.Fatal("one not loaded")
	}
	if _, ok := registry.Get("two"); !ok {
		t.Fatal("two not loaded")
	}
}

func TestRegistrySaveToDirectory(t *testing.T) {
	dir := t.TempDir()
	registry := New()
	skill := Skill{Name: "api-review", Description: "api review", Prompts: map[string]PromptTemplate{"reviewer": {User: "Review {{task_title}}."}}}
	if err := registry.SaveToDirectory(dir, skill); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "api-review.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	reloaded := New()
	if err := reloaded.LoadDirectory(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Get("api-review"); !ok {
		t.Fatal("saved skill not reloaded")
	}
}

func TestImportSkillJSONFromHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"http-skill","description":"from http","prompts":{"planner":{"user":"Plan {{task_title}}."}}}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	imported, err := importSkillJSON(context.Background(), server.URL+"/skill.json", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 1 || imported[0].Name != "http-skill" {
		t.Fatalf("imported=%+v", imported)
	}
	if _, err := os.Stat(filepath.Join(dir, "http-skill.json")); err != nil {
		t.Fatal(err)
	}
}

func TestImportSkillRepository(t *testing.T) {
	source := t.TempDir()
	skillJSON := `{"name":"repo-skill","description":"from repo","prompts":{"planner":{"user":"Plan {{task_title}}."}}}`
	if err := os.WriteFile(filepath.Join(source, "skill.json"), []byte(skillJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte(`{"name":"not-a-skill"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"add", "."},
		{"commit", "-m", "add skill"},
	} {
		cmd := exec.Command("git", command...)
		cmd.Dir = source
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(command, " "), err, output)
		}
	}
	dest := t.TempDir()
	imported, err := importSkillRepository(context.Background(), "file:///"+filepath.ToSlash(source), dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 1 || imported[0].Name != "repo-skill" {
		t.Fatalf("imported=%+v", imported)
	}
	if _, err := os.Stat(filepath.Join(dest, "repo-skill.json")); err != nil {
		t.Fatal(err)
	}
}
