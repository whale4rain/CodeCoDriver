package store

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAICompatibleEmbeddingProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization header = %q", got)
		}
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "doubao-embedding-text-240715" || len(body.Input) != 2 {
			t.Fatalf("request body = %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float64{0.5, 0.5}},
				{"index": 0, "embedding": []float64{0.1, 0.2}},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatibleEmbeddingProvider("test-key", server.URL, "doubao-embedding-text-240715", time.Second)
	vectors, err := provider.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 || len(vectors[0]) != 2 || len(vectors[1]) != 2 {
		t.Fatalf("vectors=%+v", vectors)
	}
	if vectors[0][0] != 0.1 || vectors[1][0] != 0.5 {
		t.Fatalf("vectors=%+v", vectors)
	}
	if provider.Dimensions() != doubaoEmbeddingDimensions {
		t.Fatalf("dimensions=%d", provider.Dimensions())
	}
}

func TestEmbeddingProviderReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "invalid api key"},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatibleEmbeddingProvider("bad-key", server.URL, "model", time.Second)
	_, err := provider.Embed(context.Background(), []string{"hello"})
	if err == nil || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("err=%v", err)
	}
}

func TestEmbeddingProviderFromEnvDefaultsToLocal(t *testing.T) {
	t.Setenv("CODECODRIVER_EMBEDDING_API_KEY", "")
	t.Setenv("DOUBAO_API_KEY", "")
	provider := NewEmbeddingProviderFromEnv()
	if provider.Name() != "local" {
		t.Fatalf("provider=%q", provider.Name())
	}
}
