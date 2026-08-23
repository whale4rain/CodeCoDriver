package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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

func TestDeepSeekRetriesTransientNetworkTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"content": "ok"}}},
		})
	}))
	defer server.Close()

	var roundTrips int
	client := NewDeepSeek("secret", server.URL, DefaultDeepSeekModel, &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			roundTrips++
			if roundTrips == 1 {
				return nil, &url.Error{Op: "Post", URL: req.URL.String(), Err: context.DeadlineExceeded}
			}
			return server.Client().Transport.RoundTrip(req)
		}),
	})
	client.retryBase = time.Millisecond
	client.retryMaxDelay = time.Millisecond

	got, err := client.Complete(context.Background(), "system", "ping")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Fatalf("completion=%q", got)
	}
	if roundTrips != 2 {
		t.Fatalf("round trips=%d, want 2", roundTrips)
	}
}

func TestDeepSeekRetriesTransientServerError(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"content": "recovered"}}},
		})
	}))
	defer server.Close()

	client := NewDeepSeek("secret", server.URL, DefaultDeepSeekModel, server.Client())
	client.retryBase = time.Millisecond
	client.retryMaxDelay = time.Millisecond

	got, err := client.Complete(context.Background(), "system", "ping")
	if err != nil {
		t.Fatal(err)
	}
	if got != "recovered" {
		t.Fatalf("completion=%q", got)
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}
}

func TestDeepSeekDoesNotRetryClientError(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer server.Close()

	client := NewDeepSeek("secret", server.URL, DefaultDeepSeekModel, server.Client())
	client.retryBase = time.Millisecond
	client.retryMaxDelay = time.Millisecond

	if _, err := client.Complete(context.Background(), "system", "ping"); err == nil {
		t.Fatal("expected client error")
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
}

func TestDeepSeekDoesNotRetryDecodeError(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer server.Close()

	client := NewDeepSeek("secret", server.URL, DefaultDeepSeekModel, server.Client())
	client.retryBase = time.Millisecond
	client.retryMaxDelay = time.Millisecond

	if _, err := client.Complete(context.Background(), "system", "ping"); err == nil {
		t.Fatal("expected decode error")
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
}

func TestDeepSeekAppliesRetryEnv(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "secret")
	t.Setenv("DEEPSEEK_BASE_URL", "http://localhost")
	t.Setenv("DEEPSEEK_MAX_RETRIES", "4")
	t.Setenv("DEEPSEEK_RETRY_BASE_DELAY_MS", "5000")
	t.Setenv("DEEPSEEK_RETRY_MAX_DELAY_MS", "60000")

	client, err := NewDeepSeekFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if client.maxRetries != 4 {
		t.Fatalf("maxRetries=%d, want 4", client.maxRetries)
	}
	if client.retryBase != 5*time.Second {
		t.Fatalf("retryBase=%s, want 5s", client.retryBase)
	}
	if client.retryMaxDelay != 60*time.Second {
		t.Fatalf("retryMaxDelay=%s, want 60s", client.retryMaxDelay)
	}
}

func TestEstimateCostUsesOfficialV4FlashDefaults(t *testing.T) {
	t.Setenv("DEEPSEEK_INPUT_COST_PER_MILLION", "")
	t.Setenv("DEEPSEEK_OUTPUT_COST_PER_MILLION", "")
	got := estimateCost(1_000_000, 1_000_000)
	want := 0.14 + 0.28
	if got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("estimateCost(1M,1M)=%v, want ~%v", got, want)
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
