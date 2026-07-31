package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	DefaultDeepSeekBaseURL = "https://api.deepseek.com"
	DefaultDeepSeekModel   = "deepseek-v4-flash"
)

type Client interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

type DeepSeek struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
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
	return NewDeepSeek(apiKey, baseURL, DefaultDeepSeekModel, &http.Client{Timeout: 90 * time.Second}), nil
}

func NewDeepSeek(apiKey, baseURL, model string, client *http.Client) *DeepSeek {
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	return &DeepSeek{apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), model: model, httpClient: client}
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Temperature float64   `json:"temperature"`
}
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type chatResponse struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func (d *DeepSeek) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	payload := chatRequest{Model: d.model, Messages: []message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: userPrompt}}, Temperature: 0.1}
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
	if len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("deepseek returned no completion choices")
	}
	return strings.TrimSpace(decoded.Choices[0].Message.Content), nil
}
