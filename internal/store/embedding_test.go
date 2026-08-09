package store

import (
	"math"
	"testing"
	"time"

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

func TestMemorySearchRecordsAccess(t *testing.T) {
	data := NewMemory()
	if err := data.AddMemory(domain.MemoryEntry{ID: "accessed", RepositoryID: "repo", Content: "retry timeout"}); err != nil {
		t.Fatal(err)
	}
	results, err := data.SearchMemoryLimit("repo", "retry", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].AccessCount != 1 || results[0].LastAccessedAt.IsZero() {
		t.Fatalf("access=%+v", results)
	}
}

func TestMemoryScoreFuzzyMatchesReadmeTypo(t *testing.T) {
	score := memoryScore("patch creates already-existing file README_zh.md", "中文reamdme")
	if score <= 0 {
		t.Fatalf("score=%f", score)
	}
}

func TestMemorySearchFuzzyMatchesChineseReadme(t *testing.T) {
	data := NewMemory()
	if err := data.AddMemory(domain.MemoryEntry{ID: "readme-zh", RepositoryID: "repo", Kind: "failure_pattern", Title: "中文reamdme", Content: "patch creates already-existing file README_zh.md", Summary: "patch creates already-existing file README_zh.md", Symptom: "README_zh.md", RootCause: "patch apply failure", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	results, err := data.SearchMemoryLimit("repo", "中文reamdme", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "readme-zh" {
		t.Fatalf("results=%+v", results)
	}
}
