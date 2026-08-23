package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractDiffRejectsPlainText(t *testing.T) {
	if _, err := ExtractDiff("read more files first"); err == nil {
		t.Fatal("expected error")
	}
}

func TestPreflightDiffAcceptsValidPatch(t *testing.T) {
	proposal := "```diff\n" +
		"diff --git a/a.txt b/a.txt\n" +
		"--- a/a.txt\n" +
		"+++ b/a.txt\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+new\n" +
		"```"
	if _, err := PreflightDiff(proposal); err != nil {
		t.Fatalf("preflight rejected valid patch: %v", err)
	}
}

func TestPreflightDiffRejectsRepeatedHunkHeader(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("diff --git a/a.txt b/a.txt\n")
	builder.WriteString("--- a/a.txt\n")
	builder.WriteString("+++ b/a.txt\n")
	for i := 0; i < 7; i++ {
		builder.WriteString("@@ -1 +1 @@\n-old\n+new\n")
	}
	if _, err := PreflightDiff(builder.String()); err == nil {
		t.Fatal("expected repeated hunk rejection")
	}
}

func TestRepairHunkPositionsFixesWrongStartLine(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pages_test.go"), []byte("line1\nline2\nfunc TestNewFromRequest(t *testing.T) {\nassert.Equal(t, 100, p.TotalCount)\nassert.Equal(t, 5, p.PageCount)\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff := "diff --git a/pages_test.go b/pages_test.go\n" +
		"--- a/pages_test.go\n" +
		"+++ b/pages_test.go\n" +
		"@@ -3,3 +3,55 @@ func TestNewFromRequest(t *testing.T) {\n" +
		" assert.Equal(t, 100, p.TotalCount)\n" +
		" assert.Equal(t, 5, p.PageCount)\n" +
		" }\n" +
		"+func TestNewEdgeCase(t *testing.T) {}\n"
	got := repairHunkPositions(diff, root)
	if !strings.Contains(got, "@@ -4,3 +4,55 @@") {
		t.Fatalf("repaired diff=%q", got)
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

func TestLimitOutput(t *testing.T) {
	got := limitOutput("123456", 3)
	if !strings.HasPrefix(got, "123") || !strings.Contains(got, "TRUNCATED") {
		t.Fatalf("output=%q", got)
	}
}
