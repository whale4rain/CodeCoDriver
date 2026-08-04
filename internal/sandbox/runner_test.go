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

func TestValidateAndTestRejectsNewFileWhenPathExists(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proposal := "--- /dev/null\n+++ b/existing.txt\n@@ -0,0 +1 @@\n+new\n"
	report := New(Config{}).ValidateAndTest(context.Background(), root, proposal)
	if report.Status != "invalid_patch" || !strings.Contains(report.Error, "already-existing file") {
		t.Fatalf("report=%+v", report)
	}
}

func TestValidateAndTestRejectsModifyMissingFile(t *testing.T) {
	proposal := "--- a/missing.txt\n+++ b/missing.txt\n@@ -1 +1 @@\n-old\n+new\n"
	report := New(Config{}).ValidateAndTest(context.Background(), t.TempDir(), proposal)
	if report.Status != "invalid_patch" || !strings.Contains(report.Error, "modifies missing file") {
		t.Fatalf("report=%+v", report)
	}
}

func TestValidateAndTestAcceptsMissingNewFile(t *testing.T) {
	proposal := "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\n"
	report := New(Config{}).ValidateAndTest(context.Background(), t.TempDir(), proposal)
	if !report.Applied || report.Status != "tests_skipped" {
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

func TestExtractDiffFromFenceWithGitHeader(t *testing.T) {
	fence := strings.Repeat(string(rune(96)), 3)
	proposal := "proposal\n" + fence + "diff\ndiff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n" + fence
	diff, err := ExtractDiff(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diff, fence) || !strings.HasPrefix(diff, "diff --git ") {
		t.Fatalf("diff=%q", diff)
	}
}

func TestExtractDiffFromStandaloneFence(t *testing.T) {
	fence := strings.Repeat(string(rune(96)), 3)
	proposal := fence + "\ndiff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n" + fence
	diff, err := ExtractDiff(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diff, fence) || !strings.HasPrefix(diff, "--- a/a.txt") {
		t.Fatalf("diff=%q", diff)
	}
}

func TestNormalizeDiffInsertsMissingGitHeaders(t *testing.T) {
	diff := "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-old2\n+new2\n"
	got := normalizeDiff(diff)
	if !strings.Contains(got, "diff --git a/a.go b/a.go") || !strings.Contains(got, "diff --git a/b.go b/b.go") {
		t.Fatalf("missing git headers: %s", got)
	}
	if strings.Count(got, "diff --git ") != 2 {
		t.Fatalf("unexpected header count: %s", got)
	}
}

func TestNormalizeDiffNewFileHeader(t *testing.T) {
	got := normalizeDiff("--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\n")
	if !strings.Contains(got, "diff --git a/new.txt b/new.txt") {
		t.Fatalf("new file header missing: %s", got)
	}
	if !strings.Contains(got, "new file mode 100644") {
		t.Fatalf("new file mode missing: %s", got)
	}
	if strings.Count(got, "diff --git ") != 1 {
		t.Fatalf("duplicate git headers: %s", got)
	}
}

func TestRepairHunkContextAddsTrailingContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff := "diff --git a/sample.go b/sample.go\n--- a/sample.go\n+++ b/sample.go\n@@ -1,3 +1,3 @@\n one\n-two\n+2\n"
	got := repairHunkContext(diff, root)
	if !strings.Contains(got, " three") {
		t.Fatalf("missing trailing context: %s", got)
	}
}

func TestRepairHunkContextAddsBlankTrailingContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte("one\ntwo\n\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff := "diff --git a/sample.go b/sample.go\n--- a/sample.go\n+++ b/sample.go\n@@ -1,3 +1,3 @@\n one\n-two\n+2\n"
	got := repairHunkContext(diff, root)
	if !strings.Contains(got, "\n \n") {
		t.Fatalf("missing blank trailing context: %q", got)
	}
}

func TestValidateAndTestAppliesHunkWithoutTrailingContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proposal := "--- a/sample.go\n+++ b/sample.go\n@@ -1,3 +1,3 @@\n one\n-two\n+2\n"
	report := New(Config{}).ValidateAndTest(context.Background(), root, proposal)
	if !report.Applied || report.Status != "tests_skipped" {
		t.Fatalf("report=%+v", report)
	}
}

func TestValidateAndTestAppliesMultiFileDiffWithoutGitHeaders(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("old2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proposal := "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-old2\n+new2\n"
	report := New(Config{}).ValidateAndTest(context.Background(), root, proposal)
	if !report.Applied || report.Status != "tests_skipped" {
		t.Fatalf("report=%+v", report)
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
