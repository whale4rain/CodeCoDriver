package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"codecodriver/internal/domain"
	"codecodriver/internal/llm"
	"codecodriver/internal/sandbox"
	"codecodriver/internal/tools"
)

const (
	maxAgentToolCalls               = 16
	maxPatchEditToolCalls           = 16
	maxPatchReadCallsWithoutEdit    = 8
	maxPatchRepeatedToolCall        = 3
	maxPatchEmptyGenerateAttempts   = 3
	maxPatchFinalAnswersWithoutEdit = 3
)

type agentToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func runAgentToolLoop(ctx context.Context, r AgentRequest, client llm.Client, systemPrompt, initialPrompt string, allowed map[string]bool) (string, error) {
	if client == nil {
		return "", fmt.Errorf("llm client is nil")
	}
	transcript := ""
	seenCalls := map[string]int{}
	cfg := compactConfigFromEnv()
	for i := 0; i < maxAgentToolCalls; i++ {
		content, err := client.Complete(ctx, systemPrompt, maybeCompactAgentPrompt(initialPrompt, transcript, cfg))
		if err != nil {
			return "", err
		}
		call, ok := extractAgentToolCall(content)
		if !ok {
			return content, nil
		}
		if r.Tools == nil {
			return "", fmt.Errorf("tool %s requested but no tool gateway is configured", call.Name)
		}
		if !allowed[call.Name] {
			return "", fmt.Errorf("tool %s is not allowed for agent", call.Name)
		}
		keyBytes, _ := json.Marshal(call.Arguments)
		key := call.Name + "|" + string(keyBytes)
		seenCalls[key]++
		if seenCalls[key] > 2 {
			transcript += "\n\nTOOL_RESULT_ERROR(" + call.Name + "):\nYou already have this result. Stop calling tools and provide the final answer."
			continue
		}
		args := call.Arguments
		if args == nil {
			args = map[string]any{}
		}
		args["repository_path"] = r.Repository.Path
		switch call.Name {
		case "search_files":
			args["__files"] = r.Files
		case "read_symbols":
			args["__symbols"] = r.Symbols
		case "validate_patch":
			args["__test_command"] = r.Repository.TestCommand
		}
		result, callErr := r.Tools.Call(ctx, call.Name, args)
		if callErr != nil {
			transcript += "\n\nTOOL_RESULT_ERROR(" + call.Name + "):\n" + callErr.Error()
			continue
		}
		transcript += "\n\nTOOL_RESULT(" + call.Name + "):\n" + formatAgentToolResult(result.Content)
	}
	return "", fmt.Errorf("agent tool loop exceeded %d calls", maxAgentToolCalls)
}

func extractAgentToolCall(content string) (agentToolCall, bool) {
	start := strings.Index(content, "TOOL_CALL")
	if start < 0 {
		return agentToolCall{}, false
	}
	body, ok := extractToolCallBody(content[start+len("TOOL_CALL"):])
	if !ok {
		return agentToolCall{}, false
	}
	var call agentToolCall
	if json.Unmarshal([]byte(body), &call) != nil || strings.TrimSpace(call.Name) == "" {
		return agentToolCall{}, false
	}
	return call, true
}

func extractToolCallBody(content string) (string, bool) {
	body := strings.TrimSpace(content)
	if !strings.HasPrefix(body, "{") {
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for i, r := range body {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[:i+1], true
			}
		}
	}
	return "", false
}

func formatAgentToolResult(content any) string {
	if content == nil {
		return "ok"
	}
	data, err := json.Marshal(content)
	if err != nil {
		return fmt.Sprint(content)
	}
	return string(data)
}

func patchValidationFeedback(report sandbox.Report) string {
	payload := map[string]any{
		"status":         report.Status,
		"applied":        report.Applied,
		"passed":         report.Passed,
		"test_command":   report.TestCommand,
		"changed_files":  report.ChangedFiles,
		"error":          report.Error,
		"output_excerpt": truncateFeedback(report.Output),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("status=%s applied=%v passed=%v", report.Status, report.Applied, report.Passed)
	}
	return string(data)
}

func sandboxReportPassed(report sandbox.Report, task domain.Task) bool {
	if documentationTask(task) {
		return report.Applied
	}
	return report.Applied && report.Passed
}

func agentToolInstructions(allowed map[string]bool) string {
	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	return "\n\nTOOL USE: You may call these tools before producing the final answer: " + strings.Join(names, ", ") + ". " +
		"Emit exactly one tool call as:\n" +
		"TOOL_CALL\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"pkg/example/file.go\",\"start\":1,\"end\":120}}\nTOOL_END\n" +
		"After the tool result is provided, continue. When you have enough evidence, output the final answer without a TOOL_CALL block."
}

func toolAllowList(names ...string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		out[name] = true
	}
	return out
}

func readRepositoryFileTool(_ context.Context, args map[string]any) (tools.Result, error) {
	root := stringToolArg(args, "workspace_path")
	if root == "" {
		root = stringToolArg(args, "repository_path")
	}
	path := stringToolArg(args, "path")
	if root == "" || path == "" {
		return tools.Result{}, fmt.Errorf("repository_path and path are required")
	}
	resolved, err := resolveRepositoryPath(root, path)
	if err != nil {
		return tools.Result{}, err
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		return tools.Result{}, err
	}
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	start := intToolArg(args, "start")
	end := intToolArg(args, "end")
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
	body := strings.Join(lines[start-1:end], "\n")
	return tools.Result{Content: map[string]any{"path": path, "start": start, "end": end, "lines": end - start + 1, "content": body}}, nil
}

func searchRepositoryFilesTool(_ context.Context, args map[string]any) (tools.Result, error) {
	query := strings.ToLower(stringToolArg(args, "query"))
	if query == "" {
		return tools.Result{}, fmt.Errorf("query is required")
	}
	raw, _ := args["__files"].([]domain.RepositoryFile)
	matches := []map[string]any{}
	for _, file := range raw {
		hay := strings.ToLower(file.Path + " " + file.Summary)
		if strings.Contains(hay, query) {
			matches = append(matches, map[string]any{"path": file.Path, "language": file.Language, "summary": file.Summary})
		}
	}
	return tools.Result{Content: matches}, nil
}

func readRepositorySymbolsTool(_ context.Context, args map[string]any) (tools.Result, error) {
	query := strings.ToLower(stringToolArg(args, "query"))
	if query == "" {
		return tools.Result{}, fmt.Errorf("query is required")
	}
	raw, _ := args["__symbols"].([]domain.Symbol)
	matches := []map[string]any{}
	for _, symbol := range raw {
		if strings.Contains(strings.ToLower(symbol.Name), query) || strings.Contains(strings.ToLower(symbol.FilePath), query) {
			matches = append(matches, map[string]any{"name": symbol.Name, "file": symbol.FilePath, "kind": symbol.Kind, "line": symbol.Line})
		}
	}
	return tools.Result{Content: matches}, nil
}

func validateProposalTool(ctx context.Context, args map[string]any) (tools.Result, error) {
	root := stringToolArg(args, "repository_path")
	proposal := stringToolArg(args, "proposal")
	if root == "" || proposal == "" {
		return tools.Result{}, fmt.Errorf("repository_path and proposal are required")
	}
	config := sandbox.Config{}
	if command := stringToolArg(args, "__test_command"); command != "" {
		config.TestCommand = command
	}
	report := sandbox.New(config).ValidateAndTest(ctx, root, proposal)
	return tools.Result{Content: report}, nil
}

const editWorkspaceMaxBytes = 100 * 1024 * 1024

func prepareEditWorkspace(ctx context.Context, source string) (string, error) {
	workspace, err := os.MkdirTemp("", "codecodriver-edit-*")
	if err != nil {
		return "", err
	}
	if err := sandbox.CopyRepository(source, workspace, editWorkspaceMaxBytes); err != nil {
		os.RemoveAll(workspace)
		return "", err
	}
	if _, err := gitInWorkspace(ctx, workspace, "init", "-q"); err != nil {
		os.RemoveAll(workspace)
		return "", err
	}
	if _, err := gitInWorkspace(ctx, workspace, "add", "-A"); err != nil {
		os.RemoveAll(workspace)
		return "", err
	}
	if _, err := gitInWorkspace(ctx, workspace, "-c", "user.name=CodeCoDriver", "-c", "user.email=codecodriver@example.com", "commit", "-q", "-m", "baseline"); err != nil {
		os.RemoveAll(workspace)
		return "", err
	}
	return workspace, nil
}

func cleanupEditWorkspace(path string) {
	if path != "" {
		_ = os.RemoveAll(path)
	}
}

func gitInWorkspace(ctx context.Context, workspace string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workspace
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func generateEditPatch(ctx context.Context, workspace string) (string, error) {
	if _, err := gitInWorkspace(ctx, workspace, "add", "-A"); err != nil {
		return "", err
	}
	diff, err := gitInWorkspace(ctx, workspace, "diff", "--cached", "--binary")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(diff) == "" {
		return "", fmt.Errorf("workspace has no changes")
	}
	return diff, nil
}

func editWorkspaceFileTool(_ context.Context, args map[string]any) (tools.Result, error) {
	root := stringToolArg(args, "workspace_path")
	path := stringToolArg(args, "path")
	oldText := rawToolArg(args, "old_string")
	newText := rawToolArg(args, "new_string")
	content := rawToolArg(args, "content")
	if root == "" || path == "" || (oldText == "" && content == "") {
		return tools.Result{}, fmt.Errorf("workspace_path, path, and old_string or content are required")
	}
	resolved, err := resolveWorkspaceWritePath(root, path)
	if err != nil {
		return tools.Result{}, err
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return tools.Result{}, err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	var updated string
	if oldText == "" {
		start := intToolArg(args, "start")
		end := intToolArg(args, "end")
		lines := strings.Split(text, "\n")
		if start <= 0 {
			start = 1
		}
		if end <= 0 || end > len(lines) {
			end = len(lines)
		}
		if start > end {
			start = end
		}
		replacement := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
		before := append([]string(nil), lines[:start-1]...)
		after := append([]string(nil), lines[end:]...)
		updatedLines := append(before, replacement...)
		updatedLines = append(updatedLines, after...)
		updated = strings.Join(updatedLines, "\n")
	} else {
		oldNormalized := strings.ReplaceAll(oldText, "\r\n", "\n")
		newNormalized := strings.ReplaceAll(newText, "\r\n", "\n")
		if !strings.Contains(text, oldNormalized) {
			return tools.Result{}, fmt.Errorf("old_string not found in %s", path)
		}
		updated = strings.Replace(text, oldNormalized, newNormalized, 1)
	}
	if err := os.WriteFile(resolved, []byte(updated), 0o600); err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: map[string]any{"path": path, "changed": true, "file_lines": strings.Count(updated, "\n") + 1}}, nil
}

func writeWorkspaceFileTool(_ context.Context, args map[string]any) (tools.Result, error) {
	root := stringToolArg(args, "workspace_path")
	path := stringToolArg(args, "path")
	content := rawToolArg(args, "content")
	if root == "" || path == "" {
		return tools.Result{}, fmt.Errorf("workspace_path and path are required")
	}
	resolved, err := resolveWorkspaceWritePath(root, path)
	if err != nil {
		return tools.Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return tools.Result{}, err
	}
	if err := os.WriteFile(resolved, []byte(content), 0o600); err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: map[string]any{"path": path, "changed": true}}, nil
}

func generatePatchTool(ctx context.Context, args map[string]any) (tools.Result, error) {
	root := stringToolArg(args, "workspace_path")
	if root == "" {
		return tools.Result{}, fmt.Errorf("workspace_path is required")
	}
	diff, err := generateEditPatch(ctx, root)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: diff}, nil
}

func runPatchEditLoop(ctx context.Context, r AgentRequest, client llm.Client, systemPrompt, initialPrompt string, allowed map[string]bool) (string, error) {
	if client == nil || r.Tools == nil || r.WorkspacePath == "" {
		return "", fmt.Errorf("edit workspace tools unavailable")
	}
	transcript := ""
	edited := false
	readCallsSinceProgress := 0
	emptyGenerateAttempts := 0
	finalAnswersWithoutEdit := 0
	seenCalls := map[string]int{}
	cfg := compactConfigFromEnv()
	for i := 0; i < maxPatchEditToolCalls; i++ {
		content, err := client.Complete(ctx, systemPrompt, maybeCompactAgentPrompt(initialPrompt, transcript, cfg))
		if err != nil {
			return "", err
		}
		call, ok := extractAgentToolCall(content)
		if !ok {
			if !edited {
				finalAnswersWithoutEdit++
				if finalAnswersWithoutEdit >= maxPatchFinalAnswersWithoutEdit {
					return "", fmt.Errorf("patch edit loop stopped after %d final answers without file edits", finalAnswersWithoutEdit)
				}
				transcript += "\n\nTOOL_RESULT_ERROR(generate_patch):\nYou must call edit_file or write_file before generate_patch.\nUse read_file to inspect the file, then edit_file/write_file, then generate_patch."
				continue
			}
			result, callErr := r.Tools.Call(ctx, "generate_patch", map[string]any{"workspace_path": r.WorkspacePath})
			if callErr != nil {
				emptyGenerateAttempts++
				if emptyGenerateAttempts >= maxPatchEmptyGenerateAttempts {
					return "", fmt.Errorf("patch edit loop stopped after %d empty generate_patch attempts", emptyGenerateAttempts)
				}
				transcript += "\n\nTOOL_RESULT_ERROR(generate_patch):\n" + callErr.Error() + "\nUse edit_file/write_file to modify the workspace, then call generate_patch."
				continue
			}
			if diff, ok := result.Content.(string); ok && strings.TrimSpace(diff) != "" {
				readCallsSinceProgress = 0
				return diff, nil
			}
			emptyGenerateAttempts++
			if emptyGenerateAttempts >= maxPatchEmptyGenerateAttempts {
				return "", fmt.Errorf("patch edit loop stopped after %d empty generate_patch attempts", emptyGenerateAttempts)
			}
			transcript += "\n\nTOOL_RESULT_ERROR(generate_patch):\nworkspace has no changes\nUse edit_file/write_file to modify the workspace, then call generate_patch."
			continue
		}
		if !allowed[call.Name] {
			keyBytes, _ := json.Marshal(call.Arguments)
			key := call.Name + "|" + string(keyBytes)
			seenCalls[key]++
			if seenCalls[key] > maxPatchRepeatedToolCall {
				return "", fmt.Errorf("patch edit loop repeatedly requested disallowed tool %s", call.Name)
			}
			transcript += "\n\nTOOL_RESULT_ERROR(" + call.Name + "):\nThis tool is not available in the patch edit workflow. Use read_file, search_files, read_symbols, edit_file, write_file, then generate_patch."
			continue
		}
		if call.Name == "edit_file" || call.Name == "write_file" {
			edited = true
			readCallsSinceProgress = 0
		}
		if call.Name == "generate_patch" && !edited {
			transcript += "\n\nTOOL_RESULT_ERROR(generate_patch):\nYou must call edit_file or write_file before generate_patch.\nUse read_file to inspect the file, then edit_file/write_file, then generate_patch."
			continue
		}
		if isPatchReadTool(call.Name) {
			readCallsSinceProgress++
			if readCallsSinceProgress > maxPatchReadCallsWithoutEdit {
				return "", fmt.Errorf("patch edit loop made %d read/search calls without editing a file", readCallsSinceProgress)
			}
		}
		keyBytes, _ := json.Marshal(call.Arguments)
		key := call.Name + "|" + string(keyBytes)
		seenCalls[key]++
		if seenCalls[key] > maxPatchRepeatedToolCall {
			return "", fmt.Errorf("patch edit loop repeated tool call %s %d times", call.Name, seenCalls[key])
		}
		args := call.Arguments
		if args == nil {
			args = map[string]any{}
		}
		args["workspace_path"] = r.WorkspacePath
		args["repository_path"] = r.Repository.Path
		switch call.Name {
		case "search_files":
			args["__files"] = r.Files
		case "read_symbols":
			args["__symbols"] = r.Symbols
		}
		result, callErr := r.Tools.Call(ctx, call.Name, args)
		if callErr != nil {
			if call.Name == "generate_patch" {
				emptyGenerateAttempts++
				if emptyGenerateAttempts >= maxPatchEmptyGenerateAttempts {
					return "", fmt.Errorf("patch edit loop stopped after %d failed generate_patch attempts", emptyGenerateAttempts)
				}
			}
			transcript += "\n\nTOOL_RESULT_ERROR(" + call.Name + "):\n" + callErr.Error()
			continue
		}
		if call.Name == "generate_patch" {
			if diff, ok := result.Content.(string); ok && strings.TrimSpace(diff) != "" {
				readCallsSinceProgress = 0
				return diff, nil
			}
			emptyGenerateAttempts++
			if emptyGenerateAttempts >= maxPatchEmptyGenerateAttempts {
				return "", fmt.Errorf("patch edit loop stopped after %d empty generate_patch attempts", emptyGenerateAttempts)
			}
		}
		if call.Name != "generate_patch" && !isPatchReadTool(call.Name) {
			readCallsSinceProgress = 0
		}
		transcript += "\n\nTOOL_RESULT(" + call.Name + "):\n" + formatAgentToolResult(result.Content)
	}
	return "", fmt.Errorf("patch edit loop exceeded %d calls without producing a patch", maxPatchEditToolCalls)
}

func isPatchReadTool(name string) bool {
	return name == "read_file" || name == "search_files" || name == "read_symbols"
}

func resolveRepositoryPath(root, relative string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	candidate := filepath.Join(rootResolved, filepath.FromSlash(relative))
	candidateResolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve file path: %w", err)
	}
	rel, err := filepath.Rel(rootResolved, candidateResolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes repository root")
	}
	return candidateResolved, nil
}

func resolveWorkspaceWritePath(root, relative string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	candidate := filepath.Join(rootResolved, filepath.FromSlash(relative))
	rel, err := filepath.Rel(rootResolved, filepath.Clean(candidate))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes repository root")
	}
	return candidate, nil
}

func stringToolArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func rawToolArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func intToolArg(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		parsed, _ := strconv.Atoi(value)
		return parsed
	}
	return 0
}
