package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultDoubaoEmbeddingBaseURL = "https://ark.cn-beijing.volces.com/api/v3"
	DefaultDoubaoEmbeddingModel   = "doubao-embedding-text-240715"
	DefaultEmbeddingTimeout       = 30 * time.Second
	doubaoEmbeddingDimensions     = 2560
)

// EmbeddingProvider turns memory text into vectors. The local provider keeps
// development and tests reproducible when no hosted embedding API is configured.
type EmbeddingProvider interface {
	Embed(ctx context.Context, texts []string) ([][]float64, error)
	Name() string
	Dimensions() int
}

type localEmbeddingProvider struct{}

func (localEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float64, error) {
	vectors := make([][]float64, len(texts))
	for i, text := range texts {
		vectors[i] = textEmbedding(text)
	}
	return vectors, nil
}

func (localEmbeddingProvider) Name() string { return "local" }
func (localEmbeddingProvider) Dimensions() int {
	return embeddingDimensions
}

type openAICompatibleEmbeddingProvider struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewOpenAICompatibleEmbeddingProvider targets OpenAI-compatible /embeddings
// endpoints, including Volcano Ark's Doubao embedding API.
func NewOpenAICompatibleEmbeddingProvider(apiKey, baseURL, model string, timeout time.Duration) EmbeddingProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultDoubaoEmbeddingBaseURL
	}
	if strings.TrimSpace(model) == "" {
		model = DefaultDoubaoEmbeddingModel
	}
	if timeout <= 0 {
		timeout = DefaultEmbeddingTimeout
	}
	return &openAICompatibleEmbeddingProvider{
		apiKey:     strings.TrimSpace(apiKey),
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		model:      strings.TrimSpace(model),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// NewEmbeddingProviderFromEnv reads embedding configuration without exposing
// API keys in code. A missing key falls back to the deterministic local model.
func NewEmbeddingProviderFromEnv() EmbeddingProvider {
	providerName := strings.ToLower(strings.TrimSpace(os.Getenv("CODECODRIVER_EMBEDDING_PROVIDER")))
	if providerName == "local" {
		return localEmbeddingProvider{}
	}
	if providerName != "" && providerName != "openai" && providerName != "doubao" {
		log.Printf("unknown embedding provider %q, using local fallback", providerName)
		return localEmbeddingProvider{}
	}
	apiKey := firstNonEmptyEnv("CODECODRIVER_EMBEDDING_API_KEY", "DOUBAO_API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		return localEmbeddingProvider{}
	}
	baseURL := firstNonEmptyEnv("CODECODRIVER_EMBEDDING_BASE_URL", DefaultDoubaoEmbeddingBaseURL)
	model := firstNonEmptyEnv("CODECODRIVER_EMBEDDING_MODEL", DefaultDoubaoEmbeddingModel)
	timeout := DefaultEmbeddingTimeout
	if raw := strings.TrimSpace(os.Getenv("CODECODRIVER_EMBEDDING_TIMEOUT_SECONDS")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	return NewOpenAICompatibleEmbeddingProvider(apiKey, baseURL, model, timeout)
}

func (p *openAICompatibleEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	payload := struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{
		Model: p.model,
		Input: texts,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode embedding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call embedding provider: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read embedding response: %w", err)
	}
	var decoded struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil, fmt.Errorf("decode embedding response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := http.StatusText(resp.StatusCode)
		if decoded.Error != nil && decoded.Error.Message != "" {
			detail = decoded.Error.Message
		}
		return nil, fmt.Errorf("embedding provider returned status %d: %s", resp.StatusCode, detail)
	}
	vectors := make([][]float64, len(texts))
	seen := 0
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(texts) || len(item.Embedding) == 0 {
			continue
		}
		vectors[item.Index] = item.Embedding
		seen++
	}
	if seen != len(texts) {
		return nil, fmt.Errorf("embedding provider returned %d vectors for %d inputs", seen, len(texts))
	}
	return vectors, nil
}

func (p *openAICompatibleEmbeddingProvider) Name() string { return "openai-compatible" }
func (p *openAICompatibleEmbeddingProvider) Dimensions() int {
	return doubaoEmbeddingDimensions
}

func firstNonEmptyEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func vectorLiteral(values []float64) string {
	var builder strings.Builder
	builder.WriteByte('[')
	for i, value := range values {
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	}
	builder.WriteByte(']')
	return builder.String()
}
