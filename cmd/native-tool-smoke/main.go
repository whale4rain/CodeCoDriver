package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"codecodriver/internal/llm"
)

func main() {
	client, err := llm.NewDeepSeekFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	tools := []llm.Tool{{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "echo",
			Description: "Echo a value back to the caller.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{"type": "string"},
				},
				"required": []string{"value"},
			},
		},
	}}
	messages := []llm.Message{
		{Role: "system", Content: llm.StringPtr("You are a native tool calling smoke test. Always call the echo tool with the requested value, then reply with the exact value returned by the tool.")},
		{Role: "user", Content: llm.StringPtr("Call echo with value CodeCoDriver-native-tool-ok and then reply with the value returned by the tool.")},
	}
	const expected = "CodeCoDriver-native-tool-ok"

	for attempt := 1; attempt <= 3; attempt++ {
		response, err := client.CompleteWithTools(ctx, messages, tools)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("round %d: finish_reason=%q tool_calls=%d content=%q\n", attempt, response.FinishReason, len(response.ToolCalls), response.Content)
		if len(response.ToolCalls) == 0 {
			if strings.Contains(response.Content, expected) {
				fmt.Println("NATIVE_TOOL_CALLING_OK")
				return
			}
			fmt.Println("model did not call echo; inspect output above")
			os.Exit(1)
		}

		normalized := make([]llm.ToolCall, len(response.ToolCalls))
		copy(normalized, response.ToolCalls)
		for i := range normalized {
			if strings.TrimSpace(normalized[i].ID) == "" {
				normalized[i].ID = fmt.Sprintf("call_%d", i)
			}
		}
		messages = append(messages, llm.Message{Role: "assistant", Content: llm.StringPtr(response.Content), ToolCalls: normalized})
		for _, call := range normalized {
			if call.Function.Name != "echo" {
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: llm.StringPtr("Error: unknown tool")})
				continue
			}
			var args struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: llm.StringPtr("Error: " + err.Error())})
				continue
			}
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: llm.StringPtr(args.Value)})
		}
	}
	fmt.Println("native tool calling loop did not converge")
	os.Exit(1)
}
