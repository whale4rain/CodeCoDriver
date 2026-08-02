package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeepSeekComplete(t *testing.T) {
	var gotModel, gotAuth string
	var gotMaxTokens int
	var gotThinking string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		gotModel = request.Model
		gotMaxTokens = request.MaxTokens
		gotThinking = request.Thinking.Type
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "pong"}}}})
	}))
	defer server.Close()
	client := NewDeepSeek("secret", server.URL, DefaultDeepSeekModel, server.Client())
	got, err := client.Complete(context.Background(), "system", "ping")
	if err != nil {
		t.Fatal(err)
	}
	if got != "pong" {
		t.Fatalf("completion=%q", got)
	}
	if gotModel != "deepseek-v4-flash" {
		t.Fatalf("model=%q", gotModel)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("authorization header=%q", gotAuth)
	}
	if gotMaxTokens != DefaultMaxTokens {
		t.Fatalf("max_tokens=%d", gotMaxTokens)
	}
	if gotThinking != "disabled" {
		t.Fatalf("thinking.type=%q", gotThinking)
	}
}

func TestDeepSeekReportsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "ok"}}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14}})
	}))
	defer server.Close()
	client := NewDeepSeek("secret", server.URL, DefaultDeepSeekModel, server.Client())
	var usage Usage
	client.SetUsageObserver(func(value Usage) { usage = value })
	if _, err := client.Complete(WithExecutionContext(context.Background(), "task", "run", "step", "planner"), "system", "prompt"); err != nil {
		t.Fatal(err)
	}
	if usage.TaskID != "task" || usage.AgentName != "planner" || usage.TotalTokens != 14 || usage.PromptTokens != 10 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestTimeoutFromEnv(t *testing.T) {
	t.Setenv("DEEPSEEK_TIMEOUT_SECONDS", "240")
	got, err := timeoutFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got != 240*time.Second {
		t.Fatalf("timeout=%s", got)
	}
	t.Setenv("DEEPSEEK_TIMEOUT_SECONDS", "invalid")
	if _, err := timeoutFromEnv(); err == nil {
		t.Fatal("expected invalid timeout error")
	}
}
