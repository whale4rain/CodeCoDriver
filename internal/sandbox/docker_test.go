package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDockerValidateAndTest(t *testing.T) {
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
	proposal := "--- a/sample.go\n+++ b/sample.go\n@@ -1,3 +1,3 @@\n package sample\n \n-func Value() int { return 1 }\n+func Value() int { return 2 }\n"
	runner := NewDocker(Config{
		TestCommand:    "go test ./...",
		Image:          os.Getenv("CODECODRIVER_SANDBOX_IMAGE"),
		CommandTimeout: 90 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	report := runner.ValidateAndTest(ctx, root, proposal)
	if report.Status != "passed" || !report.Applied || !report.Passed {
		t.Fatalf("report=%+v output=%s", report, report.Output)
	}
	content, err := os.ReadFile(filepath.Join(root, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "package sample\n\nfunc Value() int { return 1 }\n" {
		t.Fatal("Docker sandbox modified the original repository")
	}
}

func TestDockerValidateAndTestCRLF(t *testing.T) {
	if os.Getenv("CODECODRIVER_RUN_DOCKER_TESTS") != "1" {
		t.Skip("set CODECODRIVER_RUN_DOCKER_TESTS=1 to run Docker sandbox tests")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module sample\r\n\r\ngo 1.24\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte("package sample\r\n\r\nfunc Value() int { return 1 }\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proposal := "--- a/sample.go\n+++ b/sample.go\n@@ -1,3 +1,3 @@\n package sample\n \n-func Value() int { return 1 }\n+func Value() int { return 2 }\n"
	runner := NewDocker(Config{
		TestCommand:    "go test ./...",
		Image:          os.Getenv("CODECODRIVER_SANDBOX_IMAGE"),
		CommandTimeout: 90 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	report := runner.ValidateAndTest(ctx, root, proposal)
	if report.Status != "passed" || !report.Applied || !report.Passed {
		t.Fatalf("report=%+v output=%s", report, report.Output)
	}
}
