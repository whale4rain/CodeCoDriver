package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"codecodriver/internal/domain"
	"codecodriver/internal/sandbox"
	"codecodriver/internal/tools"
)

func TestExtractAgentToolCall(t *testing.T) {
	content := "Let me inspect.\nTOOL_CALL\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"a.go\"}}\nTOOL_END\n"
	call, ok := extractAgentToolCall(content)
	if !ok || call.Name != "read_file" || call.Arguments["path"] != "a.go" {
		t.Fatalf("call=%+v ok=%v", call, ok)
	}
	missingEnd := "TOOL_CALL\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"a.go\"}}\n"
	call, ok = extractAgentToolCall(missingEnd)
	if !ok || call.Name != "read_file" || call.Arguments["path"] != "a.go" {
		t.Fatalf("call without TOOL_END: call=%+v ok=%v", call, ok)
	}
}

func TestRunAgentToolLoopCallsTools(t *testing.T) {
	gateway := tools.NewGateway()
	_ = gateway.Register(tools.LocalTool{
		ToolName: "echo",
		Handler: func(_ context.Context, args map[string]any) (tools.Result, error) {
			return tools.Result{Content: args["value"]}, nil
		},
	})
	fake := &recordingLLM{responses: []string{
		"TOOL_CALL\n{\"name\":\"echo\",\"arguments\":{\"value\":\"hello\"}}\nTOOL_END",
		"final answer",
	}}
	request := AgentRequest{
		Task:       domain.Task{Title: "test", Description: "test"},
		Repository: domain.Repository{Path: t.TempDir()},
		Tools:      gateway,
	}
	got, err := runAgentToolLoop(context.Background(), request, fake, "system", "prompt", toolAllowList("echo"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "final answer" {
		t.Fatalf("got=%q", got)
	}
	if len(fake.prompts) != 2 || !strings.Contains(fake.prompts[1], "TOOL_RESULT(echo)") {
		t.Fatalf("prompts=%+v", fake.prompts)
	}
}

type fakeWorkspace struct {
	root       string
	resetCalls int
	closeCalls int
}

func newFakeWorkspace(t *testing.T, source string) *fakeWorkspace {
	t.Helper()
	root := t.TempDir()
	copyTestDir(t, source, root)
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "CodeCoDriver Test")
	runGit(t, root, "config", "core.autocrlf", "false")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "--allow-empty", "-q", "-m", "baseline")
	return &fakeWorkspace{root: root}
}

func (w *fakeWorkspace) ReadFile(_ context.Context, path string, start, end int) (map[string]any, error) {
	rel, err := testWorkspaceRelativePath(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(w.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		start = len(lines)
	}
	if start > end {
		start = end
	}
	return map[string]any{
		"path":    rel,
		"start":   start,
		"end":     end,
		"lines":   end - start + 1,
		"content": strings.Join(lines[start-1:end], "\n"),
	}, nil
}

func (w *fakeWorkspace) SearchFiles(_ context.Context, query string, maxRows int) ([]map[string]any, error) {
	return []map[string]any{{
		"path":     "sample.go",
		"line":     1,
		"content":  query,
		"language": "go",
	}}, nil
}

func (w *fakeWorkspace) ReadSymbols(_ context.Context, query string, maxRows int) ([]map[string]any, error) {
	return []map[string]any{{
		"name":    "Value",
		"file":    "sample.go",
		"kind":    "symbol",
		"line":    3,
		"content": query,
	}}, nil
}

func (w *fakeWorkspace) EditFile(_ context.Context, path, oldText, newText, content string, start, end int) (map[string]any, error) {
	rel, err := testWorkspaceRelativePath(path)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(w.root, filepath.FromSlash(rel))
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	updated := ""
	if oldText != "" {
		old := strings.ReplaceAll(oldText, "\r\n", "\n")
		if !strings.Contains(text, old) {
			return nil, fmt.Errorf("old_string not found in %s", rel)
		}
		updated = strings.Replace(text, old, strings.ReplaceAll(newText, "\r\n", "\n"), 1)
	} else {
		replacement := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
		for len(replacement) > 0 && replacement[len(replacement)-1] == "" {
			replacement = replacement[:len(replacement)-1]
		}
		lines := strings.Split(text, "\n")
		if start <= 0 {
			start = 1
		}
		if end <= 0 || end > len(lines) {
			end = len(lines)
		}
		if start > len(lines) {
			start = len(lines)
		}
		if start > end {
			start = end
		}
		existing := lines[start-1 : end]
		alreadyPresent := testEditBlocksEqual(existing, replacement)
		if !alreadyPresent && start-1+len(replacement) <= len(lines) {
			alreadyPresent = testEditBlocksEqual(lines[start-1:start-1+len(replacement)], replacement)
		}
		if !alreadyPresent && len(replacement) > 0 {
			present := true
			for _, line := range replacement {
				if strings.TrimSpace(line) == "" {
					continue
				}
				found := false
				for _, candidate := range lines {
					if candidate == line {
						found = true
						break
					}
				}
				if !found {
					present = false
					break
				}
			}
			alreadyPresent = present
		}
		if alreadyPresent {
			return map[string]any{
				"path":       rel,
				"changed":    false,
				"file_lines": len(lines),
				"reason":     "requested content already present at range",
			}, nil
		}
		parts := make([]string, 0, len(lines)+len(replacement))
		parts = append(parts, lines[:start-1]...)
		parts = append(parts, replacement...)
		parts = append(parts, lines[end:]...)
		updated = strings.Join(parts, "\n")
	}
	if updated == text {
		return map[string]any{
			"path":       rel,
			"changed":    false,
			"file_lines": len(strings.Split(text, "\n")),
			"reason":     "edit result is identical to current content",
		}, nil
	}
	if strings.Contains(string(raw), "\r\n") {
		updated = strings.ReplaceAll(updated, "\n", "\r\n")
	}
	if err := os.WriteFile(full, []byte(updated), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{
		"path":       rel,
		"changed":    true,
		"file_lines": len(strings.Split(updated, "\n")),
	}, nil
}

func (w *fakeWorkspace) WriteFile(_ context.Context, path, content string) (map[string]any, error) {
	rel, err := testWorkspaceRelativePath(path)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(w.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, err
	}
	ending := "\n"
	if raw, err := os.ReadFile(full); err == nil && strings.Contains(string(raw), "\r\n") {
		ending = "\r\n"
	}
	if ending == "\r\n" {
		content = strings.ReplaceAll(content, "\n", "\r\n")
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{"path": rel, "changed": true}, nil
}

func (w *fakeWorkspace) GeneratePatch(_ context.Context) (string, error) {
	if _, err := gitOutput(w.root, "add", "-A"); err != nil {
		return "", err
	}
	output, err := gitOutput(w.root, "diff", "--cached", "--binary")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(output) == "" {
		return "", fmt.Errorf("workspace has no changes")
	}
	return output, nil
}

func (w *fakeWorkspace) Reset(_ context.Context) error {
	w.resetCalls++
	if _, err := gitOutput(w.root, "reset", "--hard", "-q", "HEAD"); err != nil {
		return err
	}
	_, err := gitOutput(w.root, "clean", "-fdq")
	return err
}

func (w *fakeWorkspace) RunTest(_ context.Context, _ string) sandbox.Report {
	return sandbox.Report{Status: "passed", Applied: true, Passed: true}
}

func (w *fakeWorkspace) Close(_ context.Context) error {
	w.closeCalls++
	return nil
}

func TestPatchEditLoopRecoversFromDisallowedTool(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "sample.go"), []byte("package sample\n\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := newFakeWorkspace(t, source)
	ctx := tools.WithWorkspaceContext(context.Background(), workspace)

	gateway := tools.NewGateway()
	_ = gateway.Register(tools.LocalTool{ToolName: "edit_file", Handler: editWorkspaceFileTool})
	_ = gateway.Register(tools.LocalTool{ToolName: "generate_patch", Handler: generatePatchTool})
	fake := &recordingLLM{responses: []string{
		"TOOL_CALL\n{\"name\":\"run_test\",\"arguments\":{}}\nTOOL_END",
		"TOOL_CALL\n{\"name\":\"edit_file\",\"arguments\":{\"path\":\"sample.go\",\"old_string\":\"func Value() int { return 1 }\",\"new_string\":\"func Value() int { return 2 }\"}}\nTOOL_END",
		"final answer",
	}}
	request := AgentRequest{
		Task:       domain.Task{Title: "edit", Description: "edit sample"},
		Repository: domain.Repository{Path: source},
		Tools:      gateway,
		Workspace:  workspace,
	}
	got, err := runPatchEditLoop(ctx, request, fake, "system", "prompt", toolAllowList("edit_file", "generate_patch"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "a/sample.go") || !strings.Contains(got, "return 2") {
		t.Fatalf("diff=%q", got)
	}
	if !strings.Contains(fake.prompts[1], "not available in the patch edit workflow") {
		t.Fatalf("disallowed tool feedback missing: %q", fake.prompts[1])
	}
}

func TestPatchEditLoopIgnoresRepeatedExactEdit(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "sample.go"), []byte("package sample\n\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := newFakeWorkspace(t, source)
	ctx := tools.WithWorkspaceContext(context.Background(), workspace)

	gateway := tools.NewGateway()
	_ = gateway.Register(tools.LocalTool{ToolName: "edit_file", Handler: editWorkspaceFileTool})
	_ = gateway.Register(tools.LocalTool{ToolName: "generate_patch", Handler: generatePatchTool})
	editCall := "TOOL_CALL\n{\"name\":\"edit_file\",\"arguments\":{\"path\":\"sample.go\",\"old_string\":\"func Value() int { return 1 }\",\"new_string\":\"func Value() int { return 2 }\"}}\nTOOL_END"
	fake := &recordingLLM{responses: []string{
		editCall,
		editCall,
		"final answer",
	}}
	request := AgentRequest{
		Task:       domain.Task{Title: "edit", Description: "edit sample"},
		Repository: domain.Repository{Path: source},
		Tools:      gateway,
		Workspace:  workspace,
	}
	got, err := runPatchEditLoop(ctx, request, fake, "system", "prompt", toolAllowList("edit_file", "generate_patch"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "+func Value() int { return 2 }") != 1 {
		t.Fatalf("diff contains repeated edit: %q", got)
	}
	raw, err := os.ReadFile(filepath.Join(workspace.root, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "return 2") != 1 {
		t.Fatalf("workspace contains repeated edit: %q", raw)
	}
	if !strings.Contains(fake.prompts[2], "already applied") {
		t.Fatalf("repeated edit feedback missing: %q", fake.prompts[2])
	}
}

func TestPatchEditLoopStopsWhenNoFileEditHappens(t *testing.T) {
	workspace := newFakeWorkspace(t, t.TempDir())
	ctx := tools.WithWorkspaceContext(context.Background(), workspace)
	gateway := tools.NewGateway()
	_ = gateway.Register(tools.LocalTool{ToolName: "generate_patch", Handler: generatePatchTool})
	fake := &recordingLLM{responses: []string{"final answer", "final answer", "final answer"}}
	request := AgentRequest{
		Task:       domain.Task{Title: "edit", Description: "edit sample"},
		Repository: domain.Repository{Path: t.TempDir()},
		Tools:      gateway,
		Workspace:  workspace,
	}
	_, err := runPatchEditLoop(ctx, request, fake, "system", "prompt", toolAllowList("edit_file", "generate_patch"))
	if err == nil || !strings.Contains(err.Error(), "without file edits") {
		t.Fatalf("error=%v", err)
	}
}

func TestEditWorkspaceGeneratesGitDiff(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "sample.go"), []byte("package sample\n\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := newFakeWorkspace(t, source)
	ctx := tools.WithWorkspaceContext(context.Background(), workspace)
	result, err := editWorkspaceFileTool(ctx, map[string]any{
		"path":       "sample.go",
		"old_string": "func Value() int { return 1 }",
		"new_string": "func Value() int { return 2 }",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Content.(map[string]any); !ok {
		t.Fatalf("edit result=%+v", result)
	}
	diffResult, err := generatePatchTool(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	diff, ok := diffResult.Content.(string)
	if !ok || !strings.Contains(diff, "a/sample.go") || !strings.Contains(diff, "+func Value() int { return 2 }") {
		t.Fatalf("diff=%q", diff)
	}
}

func TestEditWorkspaceContentReplacementDoesNotDuplicateLine(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "sample.go"), []byte("line1\nline2\ntarget\nnext\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := newFakeWorkspace(t, source)
	ctx := tools.WithWorkspaceContext(context.Background(), workspace)
	_, err := editWorkspaceFileTool(ctx, map[string]any{
		"path":    "sample.go",
		"content": "target\nadded\n",
		"start":   3,
		"end":     3,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace.root, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Count(got, "target") != 1 || strings.Count(got, "added") != 1 {
		t.Fatalf("unexpected file after line replacement: %q", got)
	}
}

func TestEditWorkspacePreservesCRLF(t *testing.T) {
	source := t.TempDir()
	original := "package sample\r\n\r\nfunc Value() int { return 1 }\r\n"
	if err := os.WriteFile(filepath.Join(source, "sample.go"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := newFakeWorkspace(t, source)
	ctx := tools.WithWorkspaceContext(context.Background(), workspace)
	_, err := editWorkspaceFileTool(ctx, map[string]any{
		"path":       "sample.go",
		"old_string": "func Value() int { return 1 }",
		"new_string": "func Value() int { return 2 }",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace.root, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") || !strings.Contains(got, "\r\n") {
		t.Fatalf("CRLF line endings were not preserved: %q", got)
	}
	if strings.Contains(got, "return 1") || !strings.Contains(got, "return 2") {
		t.Fatalf("edit was not applied: %q", got)
	}
}

func TestEditWorkspaceContentReplacementIsIdempotent(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "sample.go"), []byte("line1\nline2\ntarget\nnext\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := newFakeWorkspace(t, source)
	ctx := tools.WithWorkspaceContext(context.Background(), workspace)
	args := map[string]any{
		"path":    "sample.go",
		"content": "target\nadded\n",
		"start":   3,
		"end":     3,
	}
	if _, err := editWorkspaceFileTool(ctx, args); err != nil {
		t.Fatal(err)
	}
	result, err := editWorkspaceFileTool(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	content, ok := result.Content.(map[string]any)
	if !ok || content["changed"] != false {
		t.Fatalf("second identical edit was applied: %+v", result.Content)
	}
	raw, err := os.ReadFile(filepath.Join(workspace.root, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "added") != 1 {
		t.Fatalf("duplicate content was inserted: %q", raw)
	}
}

func copyTestDir(t *testing.T, source, target string) {
	t.Helper()
	if err := filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(target, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(target, rel), data, info.Mode())
	}); err != nil {
		t.Fatal(err)
	}
}

func testWorkspaceRelativePath(path string) (string, error) {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	path = strings.TrimPrefix(path, "./")
	if path == "" || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") {
		return "", fmt.Errorf("path escapes workspace: %q", path)
	}
	return path, nil
}

func testEditBlocksEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	output, err := gitOutput(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	return string(output), err
}
