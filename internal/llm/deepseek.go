package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultDeepSeekBaseURL = "https://api.deepseek.com"
	DefaultDeepSeekModel   = "deepseek-v4-flash"
	DefaultDeepSeekTimeout = 180 * time.Second
	DefaultMaxTokens       = 8192
)

type Client interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

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
	return NewDeepSeek(apiKey, baseURL, DefaultDeepSeekModel, &http.Client{Timeout: timeout}), nil
}

func NewDeepSeek(apiKey, baseURL, model string, client *http.Client) *DeepSeek {
	if client == nil {
		client = &http.Client{Timeout: DefaultDeepSeekTimeout}
	}
	return &DeepSeek{apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), model: model, maxTokens: DefaultMaxTokens, httpClient: client}
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
	Thinking    thinking  `json:"thinking"`
}
type thinking struct {
	Type string `json:"type"`
}
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type chatResponse struct {
	Choices []struct {
		Message struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content,omitempty"`
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

func (d *DeepSeek) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	started := time.Now()
	payload := chatRequest{Model: d.model, Messages: []message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: userPrompt}}, Temperature: 0.1, MaxTokens: d.maxTokens, Thinking: thinking{Type: "disabled"}}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode deepseek request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create deepseek request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call deepseek: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("read deepseek response: %w", err)
	}
	var decoded chatResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return "", fmt.Errorf("decode deepseek response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := http.StatusText(resp.StatusCode)
		if decoded.Error != nil && decoded.Error.Message != "" {
			detail = decoded.Error.Message
		}
		return "", fmt.Errorf("deepseek returned status %d: %s", resp.StatusCode, detail)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("deepseek returned no completion choices")
	}
	if strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("deepseek returned empty content: finish_reason=%q reasoning_bytes=%d", decoded.Choices[0].FinishReason, len(decoded.Choices[0].Message.ReasoningContent))
	}
	if d.usageObserver != nil {
		total := decoded.Usage.TotalTokens
		if total == 0 {
			total = decoded.Usage.PromptTokens + decoded.Usage.CompletionTokens
		}
		d.usageObserver(Usage{TaskID: contextValue(ctx, taskKey), RunID: contextValue(ctx, runKey), StepID: contextValue(ctx, stepKey), AgentName: contextValue(ctx, agentKey), Model: d.model, PromptTokens: decoded.Usage.PromptTokens, CompletionTokens: decoded.Usage.CompletionTokens, TotalTokens: total, EstimatedCostUSD: estimateCost(decoded.Usage.PromptTokens, decoded.Usage.CompletionTokens), LatencyMS: time.Since(started).Milliseconds()})
	}
	return strings.TrimSpace(decoded.Choices[0].Message.Content), nil
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
		input = 0.27
	}
	if output == 0 {
		output = 1.10
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
