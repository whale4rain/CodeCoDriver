package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDockerWorkspaceFileTools(t *testing.T) {
	if os.Getenv("CODECODRIVER_RUN_DOCKER_TESTS") != "1" {
		t.Skip("set CODECODRIVER_RUN_DOCKER_TESTS=1 to run Docker sandbox tests")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module sample\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte("package sample\n\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample_test.go"), []byte("package sample\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) {\n\tif got := Value(); got != 2 {\n\t\tt.Fatalf(\"Value() = %d, want 2\", got)\n\t}\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	workspace, err := NewDockerWorkspace(ctx, root, Config{
		Image:          os.Getenv("CODECODRIVER_SANDBOX_IMAGE"),
		CommandTimeout: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("create docker workspace: %v", err)
	}
	defer workspace.Close(context.Background())

	read, err := workspace.ReadFile(ctx, "sample.go", 1, 10)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if content, ok := read["content"].(string); !ok || !strings.Contains(content, "func Value() int { return 1 }") {
		t.Fatalf("read content=%v", read)
	}

	matches, err := workspace.SearchFiles(ctx, "Value", 10)
	if err != nil {
		t.Fatalf("search files: %v", err)
	}
	foundSample := false
	for _, match := range matches {
		if match["path"] == "sample.go" {
			foundSample = true
			break
		}
	}
	if !foundSample {
		t.Fatalf("search did not return sample.go: %+v", matches)
	}

	symbols, err := workspace.ReadSymbols(ctx, "func", 10)
	if err != nil {
		t.Fatalf("read symbols: %v", err)
	}
	foundSymbol := false
	for _, symbol := range symbols {
		if symbol["name"] == "Value" && symbol["kind"] == "symbol" {
			foundSymbol = true
			break
		}
	}
	if !foundSymbol {
		t.Fatalf("symbol Value not found: %+v", symbols)
	}

	edit, err := workspace.EditFile(ctx, "sample.go", "func Value() int { return 1 }", "func Value() int { return 2 }", "", 0, 0)
	if err != nil {
		t.Fatalf("edit file: %v", err)
	}
	if edit["changed"] != true {
		t.Fatalf("edit did not change file: %+v", edit)
	}
	if _, err := workspace.WriteFile(ctx, "docs/notes.md", "# Notes\n"); err != nil {
		t.Fatalf("write file: %v", err)
	}

	patch, err := workspace.GeneratePatch(ctx)
	if err != nil {
		t.Fatalf("generate patch: %v", err)
	}
	if !strings.Contains(patch, "sample.go") || !strings.Contains(patch, "docs/notes.md") {
		t.Fatalf("patch missing changed files:\n%s", patch)
	}

	report := workspace.RunTest(ctx, "go test ./...")
	if report.Status != "passed" || !report.Applied || !report.Passed {
		t.Fatalf("report=%+v output=%s", report, report.Output)
	}
	if len(report.ChangedFiles) != 2 {
		t.Fatalf("changed files=%+v", report.ChangedFiles)
	}

	if err := workspace.Reset(ctx); err != nil {
		t.Fatalf("reset workspace: %v", err)
	}
	afterReset, err := workspace.ReadFile(ctx, "sample.go", 1, 10)
	if err != nil {
		t.Fatalf("read after reset: %v", err)
	}
	if content, ok := afterReset["content"].(string); !ok || !strings.Contains(content, "return 1") {
		t.Fatalf("reset did not restore sample.go: %v", afterReset)
	}
	if _, err := workspace.ReadFile(ctx, "docs/notes.md", 1, 10); err == nil {
		t.Fatal("reset did not remove written file")
	}
}

func TestDockerWorkspaceEditPreservesCRLF(t *testing.T) {
	if os.Getenv("CODECODRIVER_RUN_DOCKER_TESTS") != "1" {
		t.Skip("set CODECODRIVER_RUN_DOCKER_TESTS=1 to run Docker sandbox tests")
	}
	root := t.TempDir()
	original := "package sample\r\n\r\nfunc Value() int { return 1 }\r\n"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	workspace, err := NewDockerWorkspace(ctx, root, Config{
		Image:          os.Getenv("CODECODRIVER_SANDBOX_IMAGE"),
		CommandTimeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("create docker workspace: %v", err)
	}
	defer workspace.Close(context.Background())

	if _, err := workspace.EditFile(ctx, "sample.go", "return 1", "return 2", "", 0, 0); err != nil {
		t.Fatalf("edit file: %v", err)
	}
	dockerWorkspace := workspace.(*DockerWorkspace)
	output, err := dockerWorkspace.run(ctx, "python3", "-c", "import pathlib; p=pathlib.Path('/workspace/sample.go'); data=p.read_bytes(); print(b'\\r\\n' in data)")
	if err != nil {
		t.Fatalf("inspect CRLF: %v", err)
	}
	if !strings.Contains(output, "True") {
		t.Fatalf("CRLF not preserved: %q", output)
	}
}

func TestWorkspaceRelativePathRejectsEscapes(t *testing.T) {
	for _, path := range []string{"../x", "a/../../b", "/workspace", "C:/tmp", "..\\x", "a\\..\\..\\b", ".git/config", "a/.git/HEAD"} {
		if _, err := workspaceRelativePath(path); err == nil {
			t.Errorf("workspaceRelativePath(%q) accepted escape", path)
		}
	}
	for _, path := range []string{"a.go", "docs/readme.md", "./a.go", "a\\b.go"} {
		got, err := workspaceRelativePath(path)
		if err != nil {
			t.Errorf("workspaceRelativePath(%q): %v", path, err)
			continue
		}
		if got == "" || strings.Contains(got, "..") {
			t.Errorf("workspaceRelativePath(%q)=%q", path, got)
		}
	}
}
