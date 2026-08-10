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
