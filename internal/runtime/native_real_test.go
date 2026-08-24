package runtime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/llm"
	"codecodriver/internal/tools"
)

// TestRealNativeToolLoop verifies the runtime native tool loop against a live
// OpenAI-compatible API. It is skipped by default and requires:
//   - DEEPSEEK_API_KEY (and DEEPSEEK_BASE_URL when using a compatible gateway)
//   - RUN_NATIVE_TOOL_TEST=1
func TestRealNativeToolLoop(t *testing.T) {
	if os.Getenv("RUN_NATIVE_TOOL_TEST") != "1" {
		t.Skip("set RUN_NATIVE_TOOL_TEST=1 to run against a live DeepSeek-compatible API")
	}
	client, err := llm.NewDeepSeekFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	gateway := tools.NewGateway()
	if err := gateway.RegisterWithSchema(tools.LocalTool{
		ToolName: "echo",
		Handler: func(_ context.Context, args map[string]any) (tools.Result, error) {
			value, _ := args["value"].(string)
			return tools.Result{Content: value}, nil
		},
	}, tools.ToolSpec{
		Name:        "echo",
		Description: "Echo a value back to the caller.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string"},
			},
			"required": []string{"value"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	const expected = "CodeCoDriver-runtime-native-ok"
	request := AgentRequest{
		Task:  domain.Task{Title: "native tool integration test"},
		Tools: gateway,
	}
	got, err := runAgentToolLoop(ctx, request, client,
		"You are a native tool calling integration test. Always call echo with the requested value, then reply with the exact value returned by the tool.",
		"Call echo with value "+expected+" and then reply with that value.",
		toolAllowList("echo"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, expected) {
		t.Fatalf("got=%q", got)
	}
	t.Log(got)
}
