package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codecodriver/internal/domain"
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

func TestPatchEditLoopRecoversFromDisallowedTool(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "sample.go"), []byte("package sample\n\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := prepareEditWorkspace(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupEditWorkspace(workspace)

	gateway := tools.NewGateway()
	_ = gateway.Register(tools.LocalTool{ToolName: "edit_file", Handler: editWorkspaceFileTool})
	_ = gateway.Register(tools.LocalTool{ToolName: "generate_patch", Handler: generatePatchTool})
	fake := &recordingLLM{responses: []string{
		"TOOL_CALL\n{\"name\":\"run_test\",\"arguments\":{}}\nTOOL_END",
		"TOOL_CALL\n{\"name\":\"edit_file\",\"arguments\":{\"path\":\"sample.go\",\"old_string\":\"func Value() int { return 1 }\",\"new_string\":\"func Value() int { return 2 }\"}}\nTOOL_END",
		"final answer",
	}}
	request := AgentRequest{
		Task:          domain.Task{Title: "edit", Description: "edit sample"},
		Repository:    domain.Repository{Path: source},
		Tools:         gateway,
		WorkspacePath: workspace,
	}
	got, err := runPatchEditLoop(context.Background(), request, fake, "system", "prompt", toolAllowList("edit_file", "generate_patch"))
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
	workspace, err := prepareEditWorkspace(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupEditWorkspace(workspace)

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
		Task:          domain.Task{Title: "edit", Description: "edit sample"},
		Repository:    domain.Repository{Path: source},
		Tools:         gateway,
		WorkspacePath: workspace,
	}
	got, err := runPatchEditLoop(context.Background(), request, fake, "system", "prompt", toolAllowList("edit_file", "generate_patch"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "+func Value() int { return 2 }") != 1 {
		t.Fatalf("diff contains repeated edit: %q", got)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "sample.go"))
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
	workspace := t.TempDir()
	gateway := tools.NewGateway()
	_ = gateway.Register(tools.LocalTool{ToolName: "generate_patch", Handler: generatePatchTool})
	fake := &recordingLLM{responses: []string{"final answer", "final answer", "final answer"}}
	request := AgentRequest{
		Task:          domain.Task{Title: "edit", Description: "edit sample"},
		Repository:    domain.Repository{Path: t.TempDir()},
		Tools:         gateway,
		WorkspacePath: workspace,
	}
	_, err := runPatchEditLoop(context.Background(), request, fake, "system", "prompt", toolAllowList("edit_file", "generate_patch"))
	if err == nil || !strings.Contains(err.Error(), "without file edits") {
		t.Fatalf("error=%v", err)
	}
}

func TestEditWorkspaceGeneratesGitDiff(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "sample.go"), []byte("package sample\n\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := prepareEditWorkspace(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupEditWorkspace(workspace)
	result, err := editWorkspaceFileTool(context.Background(), map[string]any{
		"workspace_path": workspace,
		"path":           "sample.go",
		"old_string":     "func Value() int { return 1 }",
		"new_string":     "func Value() int { return 2 }",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Content.(map[string]any); !ok {
		t.Fatalf("edit result=%+v", result)
	}
	diffResult, err := generatePatchTool(context.Background(), map[string]any{"workspace_path": workspace})
	if err != nil {
		t.Fatal(err)
	}
	diff, ok := diffResult.Content.(string)
	if !ok || !strings.Contains(diff, "a/sample.go") || !strings.Contains(diff, "+func Value() int { return 2 }") {
		t.Fatalf("diff=%q", diff)
	}
}

func TestEditWorkspaceContentReplacementDoesNotDuplicateLine(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte("line1\nline2\ntarget\nnext\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := editWorkspaceFileTool(context.Background(), map[string]any{
		"workspace_path": workspace,
		"path":           "sample.go",
		"content":        "target\nadded\n",
		"start":          3,
		"end":            3,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Count(got, "target") != 1 || strings.Count(got, "added") != 1 {
		t.Fatalf("unexpected file after line replacement: %q", got)
	}
}

func TestEditWorkspacePreservesCRLF(t *testing.T) {
	workspace := t.TempDir()
	original := "package sample\r\n\r\nfunc Value() int { return 1 }\r\n"
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := editWorkspaceFileTool(context.Background(), map[string]any{
		"workspace_path": workspace,
		"path":           "sample.go",
		"old_string":     "func Value() int { return 1 }",
		"new_string":     "func Value() int { return 2 }",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "sample.go"))
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
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte("line1\nline2\ntarget\nnext\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{
		"workspace_path": workspace,
		"path":           "sample.go",
		"content":        "target\nadded\n",
		"start":          3,
		"end":            3,
	}
	if _, err := editWorkspaceFileTool(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	result, err := editWorkspaceFileTool(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	content, ok := result.Content.(map[string]any)
	if !ok || content["changed"] != false {
		t.Fatalf("second identical edit was applied: %+v", result.Content)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "added") != 1 {
		t.Fatalf("duplicate content was inserted: %q", raw)
	}
}
