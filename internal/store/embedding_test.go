package store

import (
	"math"
	"testing"

	"codecodriver/internal/domain"
)

func TestTextEmbeddingIsDeterministic(t *testing.T) {
	first := textEmbedding("retry timeout backoff")
	second := textEmbedding("retry timeout backoff")
	if len(first) != embeddingDimensions || len(second) != embeddingDimensions {
		t.Fatalf("dimensions=%d,%d", len(first), len(second))
	}
	if math.Abs(cosineSimilarity(first, second)-1) > 1e-9 {
		t.Fatalf("same text should have cosine similarity 1: %v", cosineSimilarity(first, second))
	}
}

func TestMemorySearchUsesHybridScore(t *testing.T) {
	data := NewMemory()
	if err := data.AddMemory(domain.MemoryEntry{ID: "semantic", RepositoryID: "repo", Content: "request deadline exceeded during retry backoff"}); err != nil {
		t.Fatal(err)
	}
	if err := data.AddMemory(domain.MemoryEntry{ID: "unrelated", RepositoryID: "repo", Content: "database schema migration"}); err != nil {
		t.Fatal(err)
	}
	results, err := data.SearchMemoryLimit("repo", "retry deadline", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "semantic" || results[0].Score <= 0 {
		t.Fatalf("results=%+v", results)
	}
}
