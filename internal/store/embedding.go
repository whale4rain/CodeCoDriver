package store

import (
	"hash/fnv"
	"math"
	"strings"
	"time"

	"codecodriver/internal/domain"
)

const embeddingDimensions = 32

// textEmbedding is a deterministic local baseline. It keeps retrieval reproducible
// and leaves the storage/query boundary ready for a hosted embedding provider later.
func textEmbedding(value string) []float64 {
	vector := make([]float64, embeddingDimensions)
	for _, term := range strings.Fields(strings.ToLower(value)) {
		if len(term) < 2 {
			continue
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(term))
		index := int(h.Sum32() % embeddingDimensions)
		vector[index]++
	}
	normalizeEmbedding(vector)
	return vector
}

func normalizeEmbedding(vector []float64) {
	var norm float64
	for _, value := range vector {
		norm += value * value
	}
	if norm == 0 {
		return
	}
	norm = math.Sqrt(norm)
	for i := range vector {
		vector[i] /= norm
	}
}

func cosineSimilarity(left, right []float64) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	var score float64
	for i := 0; i < limit; i++ {
		score += left[i] * right[i]
	}
	return score
}

func memorySearchScore(memory, query string, memoryEmbedding, queryEmbedding []float64) float64 {
	keyword := memoryScore(memory, strings.ToLower(query))
	semantic := cosineSimilarity(memoryEmbedding, queryEmbedding)
	return keyword*0.7 + semantic*0.3
}

func memoryRerankScore(memory domain.MemoryEntry, query string, queryEmbedding []float64, now time.Time) float64 {
	base := memorySearchScore(memory.Content, query, memory.Embedding, queryEmbedding)
	if query == "" {
		base = 0
	}
	age := now.Sub(memory.CreatedAt).Hours()
	if age < 0 {
		age = 0
	}
	freshness := 1 / (1 + age/(24*30))
	importance := 1 + 0.05*float64(memory.AccessCount)
	return base * (0.8 + 0.2*freshness) * importance
}
