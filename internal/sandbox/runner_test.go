package sandbox

import (
	"context"
	"os"
	"os/exec"
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

func TestExtractDiffWithInnerCodeFences(t *testing.T) {
	fence := strings.Repeat(string(rune(96)), 3)
	proposal := fence + "diff\ndiff --git a/a.md b/a.md\n--- a/a.md\n+++ b/a.md\n@@ -1 +1,4 @@\n old\n+```shell\n+echo hi\n+```\ndiff --git a/b.md b/b.md\n--- /dev/null\n+++ b/b.md\n@@ -0,0 +1 @@\n+new\n" + fence
	diff, err := ExtractDiff(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(diff, "diff --git ") != 2 || strings.HasPrefix(diff, fence) || strings.HasSuffix(diff, fence) {
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

func TestStripNumberedDiffLine(t *testing.T) {
	line, ok := stripNumberedDiffLine("  123 | +func Value() {}")
	if !ok || line != " +func Value() {}" {
		t.Fatalf("line=%q ok=%v", line, ok)
	}
	if _, ok := stripNumberedDiffLine("+func Value() {}"); ok {
		t.Fatal("plain diff line should not be stripped")
	}
}

func TestTrimTrailingAddedBlanks(t *testing.T) {
	diff := "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1,3 @@\n+new\n+\n"
	got := trimTrailingAddedBlanks(diff)
	if strings.HasSuffix(got, "+\n") || !strings.HasSuffix(got, "+new\n") {
		t.Fatalf("trimmed=%q", got)
	}
}

func TestSplitDiffFilesHandlesCRLF(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\r\n--- a/a.go\r\n+++ b/a.go\r\n@@ -1 +1 @@\r\n-old\r\n+new\r\ndiff --git b/b.go b/b.go\r\n--- /dev/null\r\n+++ b/b.go\r\n@@ -0,0 +1 @@\r\n+new\r\n"
	chunks := splitDiffFiles(diff)
	if len(chunks) != 2 {
		t.Fatalf("chunks=%d %q", len(chunks), chunks)
	}
}

func TestValidateAndTestAppliesNewFileWithoutTrailingBlank(t *testing.T) {
	proposal := "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1,3 @@\n+new\n+\n"
	report := New(Config{}).ValidateAndTest(context.Background(), t.TempDir(), proposal)
	if !report.Applied || report.Status != "tests_skipped" {
		t.Fatalf("report=%+v", report)
	}
}

func TestApplyToRepositoryAppliesAndCommits(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "original.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "CodeCoDriver Test"},
		{"add", "original.txt"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	proposal := "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1,2 @@\n+new\n+\n"
	report, err := New(Config{}).ApplyToRepository(context.Background(), root, proposal, "CodeCoDriver: apply test")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Applied {
		t.Fatalf("report=%+v", report)
	}
	content, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(content), "\r\n", "\n") != "new\n" {
		t.Fatalf("content=%q", content)
	}
	cmd := exec.Command("git", "log", "--oneline", "-1")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "CodeCoDriver: apply test") {
		t.Fatalf("commit=%s", output)
	}
	second, err := New(Config{}).ApplyToRepository(context.Background(), root, proposal, "CodeCoDriver: apply again")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Applied || second.Status != "already_applied" {
		t.Fatalf("second report=%+v", second)
	}
}

func TestApplyToRepositorySkipsAppliedFileAndCreatesNewFile(t *testing.T) {
	root := t.TempDir()
	readme := "# Go RESTful API Starter Kit (Boilerplate)\n\n[中文文档](README_zh.md)\n\n[![GoDoc]](https://example.com)\n"
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(readme), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "CodeCoDriver Test"},
		{"add", "README.md"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	proposal := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1,4 +1,5 @@\n # Go RESTful API Starter Kit (Boilerplate)\n \n+[中文文档](README_zh.md)\n+\n [![GoDoc]](https://example.com)\ndiff --git a/README_zh.md b/README_zh.md\nnew file mode 100644\n--- /dev/null\n+++ b/README_zh.md\n@@ -0,0 +1,2 @@\n+# 中文文档\n+\n"
	report, err := New(Config{}).ApplyToRepository(context.Background(), root, proposal, "CodeCoDriver: apply docs")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Applied {
		t.Fatalf("report=%+v", report)
	}
	if len(report.ChangedFiles) != 1 || report.ChangedFiles[0] != "README_zh.md" {
		t.Fatalf("changed_files=%v", report.ChangedFiles)
	}
	if _, err := os.Stat(filepath.Join(root, "README_zh.md")); err != nil {
		t.Fatal(err)
	}
}

func TestRepairHunkContextStripsNumberedPrefix(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff := "diff --git a/sample.go b/sample.go\n--- a/sample.go\n+++ b/sample.go\n@@ -1,3 +1,3 @@\n 1 | one\n 2 | -two\n 2 | +2\n 3 | three\n"
	got := repairHunkContext(diff, root)
	if strings.Contains(got, "1 |") || strings.Contains(got, "2 |") || !strings.Contains(got, " one") || !strings.Contains(got, " three") {
		t.Fatalf("numbered context not cleaned: %s", got)
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
