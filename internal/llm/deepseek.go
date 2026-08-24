package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultDeepSeekBaseURL       = "https://api.deepseek.com"
	DefaultDeepSeekModel         = "deepseek-v4-flash"
	DefaultDeepSeekTimeout       = 180 * time.Second
	DefaultDeepSeekMaxRetries    = 2
	DefaultDeepSeekRetryBase     = 2 * time.Second
	DefaultDeepSeekRetryMaxDelay = 30 * time.Second
	DefaultMaxTokens             = 8192
)

type Client interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// Tool is an OpenAI-compatible function schema sent in a chat request.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall is a structured function invocation returned by the model.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message is the OpenAI-compatible chat message shape used by native tool calls.
type Message struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// Response contains either final text, structured tool calls, or both.
type Response struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
}

func StringPtr(value string) *string { return &value }

type Usage struct {
	TaskID, RunID, StepID, AgentName            string
	Model                                       string
	PromptTokens, CompletionTokens, TotalTokens int
	EstimatedCostUSD                            float64
	LatencyMS                                   int64
}

type UsageObserver interface{ SetUsageObserver(func(Usage)) }

type DeepSeek struct {
	apiKey        string
	baseURL       string
	model         string
	maxTokens     int
	httpClient    *http.Client
	usageObserver func(Usage)
	maxRetries    int
	retryBase     time.Duration
	retryMaxDelay time.Duration
}

func NewDeepSeekFromEnv() (*DeepSeek, error) {
	apiKey := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if apiKey == "" {
		return nil, fmt.Errorf("DEEPSEEK_API_KEY is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("DEEPSEEK_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = DefaultDeepSeekBaseURL
	}
	timeout, err := timeoutFromEnv()
	if err != nil {
		return nil, err
	}
	client := NewDeepSeek(apiKey, baseURL, DefaultDeepSeekModel, &http.Client{Timeout: timeout})
	if err := client.applyRetryEnv(); err != nil {
		return nil, err
	}
	return client, nil
}

func NewDeepSeek(apiKey, baseURL, model string, client *http.Client) *DeepSeek {
	if client == nil {
		client = &http.Client{Timeout: DefaultDeepSeekTimeout}
	}
	return &DeepSeek{
		apiKey:        apiKey,
		baseURL:       strings.TrimRight(baseURL, "/"),
		model:         model,
		maxTokens:     DefaultMaxTokens,
		httpClient:    client,
		maxRetries:    DefaultDeepSeekMaxRetries,
		retryBase:     DefaultDeepSeekRetryBase,
		retryMaxDelay: DefaultDeepSeekRetryMaxDelay,
	}
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
	Thinking    thinking  `json:"thinking"`
}
type thinking struct {
	Type string `json:"type"`
}
type chatResponse struct {
	Choices []struct {
		Message struct {
			Role             string     `json:"role"`
			Content          *string    `json:"content"`
			ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
			ReasoningContent string     `json:"reasoning_content,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

type deepseekHTTPError struct {
	StatusCode int
	Message    string
}

func (e *deepseekHTTPError) Error() string {
	return fmt.Sprintf("deepseek returned status %d: %s", e.StatusCode, e.Message)
}

func (d *DeepSeek) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	messages := []Message{
		{Role: "system", Content: StringPtr(systemPrompt)},
		{Role: "user", Content: StringPtr(userPrompt)},
	}
	response, err := d.CompleteWithTools(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

// CompleteWithTools sends a full message history and optional tool schemas.
// Tools are optional so the same client can serve both planner-style agents and
// tool-using agents without a second HTTP client.
func (d *DeepSeek) CompleteWithTools(ctx context.Context, messages []Message, tools []Tool) (Response, error) {
	if len(messages) == 0 {
		return Response{}, fmt.Errorf("deepseek request requires at least one message")
	}
	started := time.Now()
	payload := chatRequest{Model: d.model, Messages: messages, Tools: tools, Temperature: 0.1, MaxTokens: d.maxTokens, Thinking: thinking{Type: "disabled"}}
	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, fmt.Errorf("encode deepseek request: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt <= d.maxRetries; attempt++ {
		if attempt > 0 {
			if err := ctx.Err(); err != nil {
				return Response{}, fmt.Errorf("call deepseek: %w", err)
			}
			delay := d.retryDelay(attempt)
			select {
			case <-ctx.Done():
				return Response{}, fmt.Errorf("call deepseek: %w", ctx.Err())
			case <-time.After(delay):
			}
		}
		response, callErr := d.completeOnce(ctx, body, started)
		if callErr == nil {
			return response, nil
		}
		lastErr = callErr
		if !retryableDeepSeekError(callErr, ctx) {
			return Response{}, callErr
		}
	}
	return Response{}, lastErr
}

func (d *DeepSeek) completeOnce(ctx context.Context, body []byte, started time.Time) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("create deepseek request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("call deepseek: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Response{}, fmt.Errorf("read deepseek response: %w", err)
	}
	var decoded chatResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return Response{}, fmt.Errorf("decode deepseek response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := http.StatusText(resp.StatusCode)
		if decoded.Error != nil && decoded.Error.Message != "" {
			detail = decoded.Error.Message
		}
		return Response{}, &deepseekHTTPError{StatusCode: resp.StatusCode, Message: detail}
	}
	if len(decoded.Choices) == 0 {
		return Response{}, fmt.Errorf("deepseek returned no completion choices")
	}
	message := decoded.Choices[0].Message
	content := ""
	if message.Content != nil {
		content = strings.TrimSpace(*message.Content)
	}
	if content == "" && len(message.ToolCalls) == 0 {
		return Response{}, fmt.Errorf("deepseek returned empty content and no tool calls: finish_reason=%q reasoning_bytes=%d", decoded.Choices[0].FinishReason, len(message.ReasoningContent))
	}
	if d.usageObserver != nil {
		total := decoded.Usage.TotalTokens
		if total == 0 {
			total = decoded.Usage.PromptTokens + decoded.Usage.CompletionTokens
		}
		d.usageObserver(Usage{TaskID: contextValue(ctx, taskKey), RunID: contextValue(ctx, runKey), StepID: contextValue(ctx, stepKey), AgentName: contextValue(ctx, agentKey), Model: d.model, PromptTokens: decoded.Usage.PromptTokens, CompletionTokens: decoded.Usage.CompletionTokens, TotalTokens: total, EstimatedCostUSD: estimateCost(decoded.Usage.PromptTokens, decoded.Usage.CompletionTokens), LatencyMS: time.Since(started).Milliseconds()})
	}
	return Response{Content: content, ToolCalls: message.ToolCalls, FinishReason: decoded.Choices[0].FinishReason}, nil
}

func retryableDeepSeekError(err error, ctx context.Context) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var httpErr *deepseekHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusRequestTimeout ||
			httpErr.StatusCode == http.StatusTooManyRequests ||
			httpErr.StatusCode >= http.StatusInternalServerError
	}
	var timeoutErr interface{ Timeout() bool }
	if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return false
}

func (d *DeepSeek) retryDelay(attempt int) time.Duration {
	delay := d.retryBase
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= d.retryMaxDelay {
			return d.retryMaxDelay
		}
	}
	if delay > d.retryMaxDelay {
		return d.retryMaxDelay
	}
	return delay
}

func (d *DeepSeek) applyRetryEnv() error {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_MAX_RETRIES")); raw != "" {
		retries, err := strconv.Atoi(raw)
		if err != nil || retries < 0 {
			return fmt.Errorf("DEEPSEEK_MAX_RETRIES must be a non-negative integer")
		}
		d.maxRetries = retries
	}
	base, err := durationMillisFromEnv("DEEPSEEK_RETRY_BASE_DELAY_MS", DefaultDeepSeekRetryBase)
	if err != nil {
		return err
	}
	maxDelay, err := durationMillisFromEnv("DEEPSEEK_RETRY_MAX_DELAY_MS", DefaultDeepSeekRetryMaxDelay)
	if err != nil {
		return err
	}
	d.retryBase = base
	d.retryMaxDelay = maxDelay
	return nil
}

func durationMillisFromEnv(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

func (d *DeepSeek) SetUsageObserver(observer func(Usage)) { d.usageObserver = observer }

type contextKey string

const (
	taskKey  contextKey = "task_id"
	runKey   contextKey = "run_id"
	stepKey  contextKey = "step_id"
	agentKey contextKey = "agent_name"
)

func WithExecutionContext(ctx context.Context, taskID, runID, stepID, agent string) context.Context {
	ctx = context.WithValue(ctx, taskKey, taskID)
	ctx = context.WithValue(ctx, runKey, runID)
	ctx = context.WithValue(ctx, stepKey, stepID)
	return context.WithValue(ctx, agentKey, agent)
}
func contextValue(ctx context.Context, key contextKey) string {
	value, _ := ctx.Value(key).(string)
	return value
}

func estimateCost(promptTokens, completionTokens int) float64 {
	input, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv("DEEPSEEK_INPUT_COST_PER_MILLION")), 64)
	output, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv("DEEPSEEK_OUTPUT_COST_PER_MILLION")), 64)
	if input == 0 {
		input = 0.14
	}
	if output == 0 {
		output = 0.28
	}
	return float64(promptTokens)*input/1_000_000 + float64(completionTokens)*output/1_000_000
}

func timeoutFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("DEEPSEEK_TIMEOUT_SECONDS"))
	if raw == "" {
		return DefaultDeepSeekTimeout, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("DEEPSEEK_TIMEOUT_SECONDS must be a positive integer")
	}
	return time.Duration(seconds) * time.Second, nil
}
