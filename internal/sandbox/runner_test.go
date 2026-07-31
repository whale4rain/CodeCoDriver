package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAndTestAppliesPatchOnlyInSandbox(t *testing.T) {
	root := t.TempDir()
	original := "package sample\n\nfunc Value() int { return 1 }\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module sample\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	proposal := "--- a/sample.go\n+++ b/sample.go\n@@ -1,3 +1,3 @@\n package sample\n \n-func Value() int { return 1 }\n+func Value() int { return 2 }\n"
	report := New(Config{}).ValidateAndTest(context.Background(), root, proposal)
	if !report.Applied || !report.Passed || report.Status != "passed" {
		t.Fatalf("report=%+v", report)
	}
	current, err := os.ReadFile(filepath.Join(root, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != original {
		t.Fatal("original repository was modified")
	}
}

func TestValidateAndTestRejectsUnsafePath(t *testing.T) {
	proposal := "--- a/ok.txt\n+++ b/../outside.txt\n@@ -0,0 +1 @@\n+bad\n"
	report := New(Config{}).ValidateAndTest(context.Background(), t.TempDir(), proposal)
	if report.Status != "invalid_patch" || !strings.Contains(report.Error, "unsafe patch path") {
		t.Fatalf("report=%+v", report)
	}
}

func TestExtractDiffRejectsPlainText(t *testing.T) {
	if _, err := ExtractDiff("read more files first"); err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractDiffFromFence(t *testing.T) {
	fence := strings.Repeat(string(rune(96)), 3)
	proposal := "proposal\n" + fence + "diff\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n" + fence
	diff, err := ExtractDiff(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diff, fence) || !strings.HasPrefix(diff, "--- a/a.txt") {
		t.Fatalf("diff=%q", diff)
	}
}

func TestValidateAndTestRejectsSensitiveFile(t *testing.T) {
	proposal := "--- a/.env\n+++ b/.env\n@@ -1 +1 @@\n-old\n+new\n"
	report := New(Config{}).ValidateAndTest(context.Background(), t.TempDir(), proposal)
	if report.Status != "invalid_patch" || !strings.Contains(report.Error, "sensitive") {
		t.Fatalf("report=%+v", report)
	}
}

func TestLimitOutput(t *testing.T) {
	got := limitOutput("123456", 3)
	if !strings.HasPrefix(got, "123") || !strings.Contains(got, "TRUNCATED") {
		t.Fatalf("output=%q", got)
	}
}

func TestValidateAndTestRecountsModelHunks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module sample\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte("package sample\n\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proposal := "--- a/sample.go\n+++ b/sample.go\n@@ -1,99 +1,99 @@\n package sample\n \n-func Value() int { return 1 }\n+func Value() int { return 2 }\n"
	report := New(Config{}).ValidateAndTest(context.Background(), root, proposal)
	if !report.Applied || !report.Passed {
		t.Fatalf("report=%+v", report)
	}
}
