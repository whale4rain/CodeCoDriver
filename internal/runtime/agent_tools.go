package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"codecodriver/internal/llm"
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

type nativeToolClient interface {
	CompleteWithTools(context.Context, []llm.Message, []llm.Tool) (llm.Response, error)
}

type nativeToolInvocation struct {
	agentToolCall
	ID string
}

func runAgentToolLoop(ctx context.Context, r AgentRequest, client llm.Client, systemPrompt, initialPrompt string, allowed map[string]bool) (string, error) {
	if client == nil {
		return "", fmt.Errorf("llm client is nil")
	}
	if native, ok := client.(nativeToolClient); ok && r.Tools != nil {
		return runNativeAgentToolLoop(ctx, r, native, systemPrompt, initialPrompt, allowed)
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
		result, callErr := r.Tools.Call(ctx, call.Name, args)
		if callErr != nil {
			transcript += "\n\nTOOL_RESULT_ERROR(" + call.Name + "):\n" + callErr.Error()
			continue
		}
		transcript += "\n\nTOOL_RESULT(" + call.Name + "):\n" + formatAgentToolResult(result.Content)
	}
	return "", fmt.Errorf("agent tool loop exceeded %d calls", maxAgentToolCalls)
}

func runNativeAgentToolLoop(ctx context.Context, r AgentRequest, client nativeToolClient, systemPrompt, initialPrompt string, allowed map[string]bool) (string, error) {
	schemas, err := nativeToolSchemas(r.Tools, allowed)
	if err != nil {
		return "", err
	}
	messages := []llm.Message{
		{Role: "system", Content: llm.StringPtr(systemPrompt)},
		{Role: "user", Content: llm.StringPtr(initialPrompt)},
	}
	seenCalls := map[string]int{}
	cfg := compactConfigFromEnv()
	for i := 0; i < maxAgentToolCalls; i++ {
		response, callErr := client.CompleteWithTools(ctx, messages, schemas)
		if callErr != nil {
			return "", callErr
		}
		toolCalls := normalizedNativeToolCalls(response.ToolCalls)
		invocations := nativeToolInvocations(toolCalls)
		roundStart := len(messages)
		messages = append(messages, llm.Message{Role: "assistant", Content: llm.StringPtr(response.Content), ToolCalls: toolCalls})
		if len(invocations) == 0 {
			if strings.TrimSpace(response.Content) != "" {
				return strings.TrimSpace(response.Content), nil
			}
			return "", fmt.Errorf("agent returned empty response without tool calls")
		}
		for _, invocation := range invocations {
			if strings.TrimSpace(invocation.Name) == "" {
				messages = appendNativeToolResult(messages, invocation.ID, "", tools.Result{}, fmt.Errorf("model returned tool call without a name"))
				continue
			}
			if !allowed[invocation.Name] {
				messages = appendNativeToolResult(messages, invocation.ID, invocation.Name, tools.Result{}, fmt.Errorf("tool %s is not allowed for agent", invocation.Name))
				continue
			}
			keyBytes, _ := json.Marshal(invocation.Arguments)
			key := invocation.Name + "|" + string(keyBytes)
			seenCalls[key]++
			if seenCalls[key] > 2 {
				messages = appendNativeToolResult(messages, invocation.ID, invocation.Name, tools.Result{}, fmt.Errorf("you already have this result. Stop calling tools and provide the final answer"))
				continue
			}
			args := invocation.Arguments
			if args == nil {
				args = map[string]any{}
			}
			result, toolErr := r.Tools.Call(ctx, invocation.Name, args)
			messages = appendNativeToolResult(messages, invocation.ID, invocation.Name, result, toolErr)
		}
		messages = compactNativeMessages(messages, cfg, roundStart)
	}
	return "", fmt.Errorf("agent tool loop exceeded %d native calls", maxAgentToolCalls)
}

func nativeToolInvocations(calls []llm.ToolCall) []nativeToolInvocation {
	out := make([]nativeToolInvocation, 0, len(calls))
	for index, call := range calls {
		id := strings.TrimSpace(call.ID)
		if id == "" {
			id = fmt.Sprintf("call_%d", index)
		}
		args := map[string]any{}
		if strings.TrimSpace(call.Function.Arguments) != "" {
			if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
				args = map[string]any{"_invalid_arguments": call.Function.Arguments}
			}
		}
		out = append(out, nativeToolInvocation{
			agentToolCall: agentToolCall{Name: strings.TrimSpace(call.Function.Name), Arguments: args},
			ID:            id,
		})
	}
	return out
}

func normalizedNativeToolCalls(calls []llm.ToolCall) []llm.ToolCall {
	out := append([]llm.ToolCall(nil), calls...)
	for index := range out {
		if strings.TrimSpace(out[index].ID) == "" {
			out[index].ID = fmt.Sprintf("call_%d", index)
		}
	}
	return out
}

func nativeToolSchemas(gateway *tools.Gateway, allowed map[string]bool) ([]llm.Tool, error) {
	if gateway == nil {
		return nil, fmt.Errorf("native tool calling requires a tool gateway")
	}
	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := gateway.ToolSpecs(names)
	out := make([]llm.Tool, 0, len(specs))
	for _, spec := range specs {
		out = append(out, llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  spec.Parameters,
			},
		})
	}
	return out, nil
}

func appendNativeToolResult(messages []llm.Message, toolCallID, name string, result tools.Result, callErr error) []llm.Message {
	content := formatAgentToolResult(result.Content)
	if callErr != nil {
		content = "Error: " + callErr.Error()
	}
	return append(messages, llm.Message{Role: "tool", ToolCallID: toolCallID, Content: llm.StringPtr(content)})
}

func compactNativeMessages(messages []llm.Message, cfg compactConfig, roundStart int) []llm.Message {
	if approximateTokenCount(nativeMessagesText(messages)) <= cfg.thresholdTokens {
		return messages
	}
	start := 2
	end := roundStart
	if end <= start || end >= len(messages) {
		return messages
	}
	older := messages[start:end]
	current := messages[end:]
	names := map[string]string{}
	var summary strings.Builder
	summary.WriteString("COMPACTED_TOOL_HISTORY:\n")
	for _, message := range older {
		for _, call := range message.ToolCalls {
			names[call.ID] = call.Function.Name
		}
	}
	for _, message := range older {
		if message.Role != "tool" || message.Content == nil {
			continue
		}
		name := names[message.ToolCallID]
		if name == "" {
			name = message.ToolCallID
		}
		fmt.Fprintf(&summary, "- %s: %d estimated tokens\n", name, approximateTokenCount(*message.Content))
	}
	summary.WriteString("Earlier tool results were compacted. Continue with the recent messages and current tool results.")
	compacted := []llm.Message{messages[0], messages[1]}
	compacted = append(compacted, llm.Message{Role: "user", Content: llm.StringPtr(summary.String())})
	compacted = append(compacted, current...)
	return compacted
}

func nativeMessagesText(messages []llm.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		if message.Content != nil {
			builder.WriteString(*message.Content)
		}
	}
	return builder.String()
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

func supportsNativeTools(client llm.Client) bool {
	_, ok := client.(nativeToolClient)
	return ok
}

func toolAllowList(names ...string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		out[name] = true
	}
	return out
}

func readRepositoryFileTool(ctx context.Context, args map[string]any) (tools.Result, error) {
	workspace, err := workspaceFromToolContext(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	path := stringToolArg(args, "path")
	if path == "" {
		return tools.Result{}, fmt.Errorf("path is required")
	}
	content, err := workspace.ReadFile(ctx, path, intToolArg(args, "start"), intToolArg(args, "end"))
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: content}, nil
}

func searchRepositoryFilesTool(ctx context.Context, args map[string]any) (tools.Result, error) {
	workspace, err := workspaceFromToolContext(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	query := stringToolArg(args, "query")
	if query == "" {
		return tools.Result{}, fmt.Errorf("query is required")
	}
	maxRows := intToolArg(args, "max_rows")
	matches, err := workspace.SearchFiles(ctx, query, maxRows)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: matches}, nil
}

func readRepositorySymbolsTool(ctx context.Context, args map[string]any) (tools.Result, error) {
	workspace, err := workspaceFromToolContext(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	query := stringToolArg(args, "query")
	if query == "" {
		return tools.Result{}, fmt.Errorf("query is required")
	}
	maxRows := intToolArg(args, "max_rows")
	matches, err := workspace.ReadSymbols(ctx, query, maxRows)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: matches}, nil
}

func workspaceFromToolContext(ctx context.Context) (tools.Workspace, error) {
	workspace := tools.WorkspaceFromContext(ctx)
	if workspace == nil {
		return nil, fmt.Errorf("file tool requires an isolated workspace context")
	}
	return workspace, nil
}

func editWorkspaceFileTool(ctx context.Context, args map[string]any) (tools.Result, error) {
	workspace, err := workspaceFromToolContext(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	path := stringToolArg(args, "path")
	oldText := rawToolArg(args, "old_string")
	newText := rawToolArg(args, "new_string")
	content := rawToolArg(args, "content")
	if path == "" || (oldText == "" && content == "") {
		return tools.Result{}, fmt.Errorf("path and old_string or content are required")
	}
	result, err := workspace.EditFile(ctx, path, oldText, newText, content, intToolArg(args, "start"), intToolArg(args, "end"))
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: result}, nil
}

func writeWorkspaceFileTool(ctx context.Context, args map[string]any) (tools.Result, error) {
	workspace, err := workspaceFromToolContext(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	path := stringToolArg(args, "path")
	content := rawToolArg(args, "content")
	if path == "" {
		return tools.Result{}, fmt.Errorf("path and content are required")
	}
	result, err := workspace.WriteFile(ctx, path, content)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: result}, nil
}

func generatePatchTool(ctx context.Context, args map[string]any) (tools.Result, error) {
	workspace, err := workspaceFromToolContext(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	diff, err := workspace.GeneratePatch(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: diff}, nil
}

func runPatchEditLoop(ctx context.Context, r AgentRequest, client llm.Client, systemPrompt, initialPrompt string, allowed map[string]bool) (string, error) {
	if client == nil || r.Tools == nil || r.Workspace == nil {
		return "", fmt.Errorf("edit workspace tools unavailable")
	}
	if native, ok := client.(nativeToolClient); ok {
		return runNativePatchEditLoop(ctx, r, native, systemPrompt, initialPrompt, allowed)
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
			result, callErr := r.Tools.Call(ctx, "generate_patch", map[string]any{})
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
		if (call.Name == "edit_file" || call.Name == "write_file") && seenCalls[key] > 1 {
			transcript += "\n\nTOOL_RESULT_ERROR(" + call.Name + "):\nThis exact edit was already applied. Read the current file if you need to verify it, then call generate_patch. Do not repeat the same edit_file/write_file call."
			continue
		}
		args := call.Arguments
		if args == nil {
			args = map[string]any{}
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

func runNativePatchEditLoop(ctx context.Context, r AgentRequest, client nativeToolClient, systemPrompt, initialPrompt string, allowed map[string]bool) (string, error) {
	schemas, err := nativeToolSchemas(r.Tools, allowed)
	if err != nil {
		return "", err
	}
	messages := []llm.Message{
		{Role: "system", Content: llm.StringPtr(systemPrompt)},
		{Role: "user", Content: llm.StringPtr(initialPrompt)},
	}
	edited := false
	readCallsSinceProgress := 0
	emptyGenerateAttempts := 0
	finalAnswersWithoutEdit := 0
	seenCalls := map[string]int{}
	cfg := compactConfigFromEnv()
	for i := 0; i < maxPatchEditToolCalls; i++ {
		response, callErr := client.CompleteWithTools(ctx, messages, schemas)
		if callErr != nil {
			return "", callErr
		}
		toolCalls := normalizedNativeToolCalls(response.ToolCalls)
		invocations := nativeToolInvocations(toolCalls)
		roundStart := len(messages)
		messages = append(messages, llm.Message{Role: "assistant", Content: llm.StringPtr(response.Content), ToolCalls: toolCalls})
		if len(invocations) == 0 {
			if !edited {
				finalAnswersWithoutEdit++
				if finalAnswersWithoutEdit >= maxPatchFinalAnswersWithoutEdit {
					return "", fmt.Errorf("patch edit loop stopped after %d final answers without file edits", finalAnswersWithoutEdit)
				}
				messages = append(messages, llm.Message{Role: "user", Content: llm.StringPtr("You must call edit_file or write_file before generate_patch. Use read_file to inspect the file, then edit_file/write_file, then generate_patch.")})
				continue
			}
			result, patchErr := r.Tools.Call(ctx, "generate_patch", map[string]any{})
			if patchErr != nil {
				emptyGenerateAttempts++
				if emptyGenerateAttempts >= maxPatchEmptyGenerateAttempts {
					return "", fmt.Errorf("patch edit loop stopped after %d empty generate_patch attempts", emptyGenerateAttempts)
				}
				messages = append(messages, llm.Message{Role: "user", Content: llm.StringPtr("generate_patch failed: " + patchErr.Error() + ". Use edit_file/write_file to modify the workspace, then call generate_patch.")})
				continue
			}
			if diff, ok := result.Content.(string); ok && strings.TrimSpace(diff) != "" {
				return diff, nil
			}
			emptyGenerateAttempts++
			if emptyGenerateAttempts >= maxPatchEmptyGenerateAttempts {
				return "", fmt.Errorf("patch edit loop stopped after %d empty generate_patch attempts", emptyGenerateAttempts)
			}
			messages = append(messages, llm.Message{Role: "user", Content: llm.StringPtr("workspace has no changes. Use edit_file/write_file to modify the workspace, then call generate_patch.")})
			continue
		}
		for _, invocation := range invocations {
			if !allowed[invocation.Name] {
				keyBytes, _ := json.Marshal(invocation.Arguments)
				key := invocation.Name + "|" + string(keyBytes)
				seenCalls[key]++
				if seenCalls[key] > maxPatchRepeatedToolCall {
					return "", fmt.Errorf("patch edit loop repeatedly requested disallowed tool %s", invocation.Name)
				}
				messages = appendNativeToolResult(messages, invocation.ID, invocation.Name, tools.Result{}, fmt.Errorf("tool %s is not available in the patch edit workflow. Use read_file, search_files, read_symbols, edit_file, write_file, then generate_patch", invocation.Name))
				continue
			}
			if invocation.Name == "edit_file" || invocation.Name == "write_file" {
				edited = true
				readCallsSinceProgress = 0
			}
			if invocation.Name == "generate_patch" && !edited {
				messages = appendNativeToolResult(messages, invocation.ID, invocation.Name, tools.Result{}, fmt.Errorf("you must call edit_file or write_file before generate_patch. Use read_file to inspect the file, then edit_file/write_file, then generate_patch"))
				continue
			}
			if isPatchReadTool(invocation.Name) {
				readCallsSinceProgress++
				if readCallsSinceProgress > maxPatchReadCallsWithoutEdit {
					return "", fmt.Errorf("patch edit loop made %d read/search calls without editing a file", readCallsSinceProgress)
				}
			}
			keyBytes, _ := json.Marshal(invocation.Arguments)
			key := invocation.Name + "|" + string(keyBytes)
			seenCalls[key]++
			if seenCalls[key] > maxPatchRepeatedToolCall {
				return "", fmt.Errorf("patch edit loop repeated tool call %s %d times", invocation.Name, seenCalls[key])
			}
			if (invocation.Name == "edit_file" || invocation.Name == "write_file") && seenCalls[key] > 1 {
				messages = appendNativeToolResult(messages, invocation.ID, invocation.Name, tools.Result{}, fmt.Errorf("this exact edit was already applied. Read the current file if you need to verify it, then call generate_patch. Do not repeat the same edit_file/write_file call"))
				continue
			}
			args := invocation.Arguments
			if args == nil {
				args = map[string]any{}
			}
			result, toolErr := r.Tools.Call(ctx, invocation.Name, args)
			if toolErr != nil {
				if invocation.Name == "generate_patch" {
					emptyGenerateAttempts++
					if emptyGenerateAttempts >= maxPatchEmptyGenerateAttempts {
						return "", fmt.Errorf("patch edit loop stopped after %d failed generate_patch attempts", emptyGenerateAttempts)
					}
				}
				messages = appendNativeToolResult(messages, invocation.ID, invocation.Name, result, toolErr)
				continue
			}
			if invocation.Name == "generate_patch" {
				if diff, ok := result.Content.(string); ok && strings.TrimSpace(diff) != "" {
					return diff, nil
				}
				emptyGenerateAttempts++
				if emptyGenerateAttempts >= maxPatchEmptyGenerateAttempts {
					return "", fmt.Errorf("patch edit loop stopped after %d empty generate_patch attempts", emptyGenerateAttempts)
				}
				messages = appendNativeToolResult(messages, invocation.ID, invocation.Name, result, fmt.Errorf("workspace has no changes. Use edit_file/write_file to modify the workspace, then call generate_patch"))
				continue
			}
			if !isPatchReadTool(invocation.Name) {
				readCallsSinceProgress = 0
			}
			messages = appendNativeToolResult(messages, invocation.ID, invocation.Name, result, nil)
		}
		messages = compactNativeMessages(messages, cfg, roundStart)
	}
	return "", fmt.Errorf("patch edit loop exceeded %d native calls without producing a patch", maxPatchEditToolCalls)
}

func isPatchReadTool(name string) bool {
	return name == "read_file" || name == "search_files" || name == "read_symbols"
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
