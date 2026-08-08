package memory

import (
	"context"
	"log"
	"math"
	"strings"

	"codecodriver/internal/domain"
)

const dedupThreshold = 0.75

func (s *Service) dedupeAll(ctx context.Context, entries []domain.MemoryEntry) {
	for _, original := range entries {
		entry, err := s.store.GetMemory(original.ID)
		if err != nil {
			log.Printf("load memory for dedup %s: %v", original.ID, err)
			continue
		}
		if !dedupCandidate(entry) {
			continue
		}
		candidates, err := s.store.SearchMemoryLimit(entry.RepositoryID, dedupQuery(entry), 10)
		if err != nil {
			log.Printf("search memories for dedup %s: %v", entry.ID, err)
			continue
		}
		for _, candidate := range candidates {
			if !sameDedupKind(entry, candidate) || candidate.DuplicateOf != "" || candidate.ID == entry.ID {
				continue
			}
			if similarityScore(entry, candidate) < dedupThreshold {
				continue
			}
			primary, secondary := pickPrimary(entry, candidate)
			if secondary.ID == primary.ID {
				continue
			}
			secondary.DuplicateOf = primary.ID
			if err := s.store.UpdateMemory(secondary); err != nil {
				log.Printf("mark duplicate memory %s -> %s: %v", secondary.ID, primary.ID, err)
				continue
			}
			s.mergeMemoryLinks(secondary.ID, primary.ID)
		}
	}
}

func dedupCandidate(entry domain.MemoryEntry) bool {
	if entry.DuplicateOf != "" || entry.ConflictGroupID != "" {
		return false
	}
	return entry.Kind == "execution_success" || entry.Kind == "failure_pattern"
}

func sameDedupKind(left, right domain.MemoryEntry) bool {
	return left.Kind == right.Kind && right.DuplicateOf == "" && right.ConflictGroupID == ""
}

func dedupQuery(entry domain.MemoryEntry) string {
	return strings.TrimSpace(entry.Title + " " + entry.Symptom + " " + entry.Summary)
}

func similarityScore(left, right domain.MemoryEntry) float64 {
	cosine := cosineSimilarity(left.Embedding, right.Embedding)
	keyword := tokenOverlap(left.Title+" "+left.Summary+" "+left.Content, right.Title+" "+right.Summary+" "+right.Content)
	fileSimilarity := jaccard(left.ChangedFiles, right.ChangedFiles)
	rootCauseSimilarity := 0.0
	if left.RootCause != "" && left.RootCause == right.RootCause {
		rootCauseSimilarity = 1
	}
	return cosine*0.35 + keyword*0.30 + fileSimilarity*0.20 + rootCauseSimilarity*0.15
}

func pickPrimary(left, right domain.MemoryEntry) (domain.MemoryEntry, domain.MemoryEntry) {
	if memoryStrength(left) >= memoryStrength(right) {
		return left, right
	}
	return right, left
}

func memoryStrength(entry domain.MemoryEntry) float64 {
	strength := entry.SuccessScore
	if entry.SuccessScore == 0 && entry.Kind == "failure_pattern" {
		strength = 0.5
	}
	if len(entry.VerificationEvidence) > 0 {
		strength += 0.25
	}
	if entry.AccessCount > 0 {
		strength += 0.1 * float64(entry.AccessCount)
	}
	return strength
}

func tokenOverlap(left, right string) float64 {
	leftTokens := tokenSet(left)
	rightTokens := tokenSet(right)
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return 0
	}
	overlap := 0
	for token := range leftTokens {
		if rightTokens[token] {
			overlap++
		}
	}
	union := len(leftTokens) + len(rightTokens) - overlap
	if union == 0 {
		return 0
	}
	return float64(overlap) / float64(union)
}

func tokenSet(value string) map[string]bool {
	out := map[string]bool{}
	for _, token := range strings.Fields(strings.ToLower(value)) {
		if len(token) >= 3 {
			out[token] = true
		}
	}
	return out
}

func jaccard(left, right []string) float64 {
	leftSet := stringSet(left)
	rightSet := stringSet(right)
	if len(leftSet) == 0 || len(rightSet) == 0 {
		return 0
	}
	overlap := 0
	for value := range leftSet {
		if rightSet[value] {
			overlap++
		}
	}
	union := len(leftSet) + len(rightSet) - overlap
	if union == 0 {
		return 0
	}
	return float64(overlap) / float64(union)
}

func stringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[strings.ToLower(strings.TrimSpace(value))] = true
	}
	return out
}

func cosineSimilarity(left, right []float64) float64 {
	if len(left) == 0 || len(right) == 0 || len(left) != len(right) {
		return 0
	}
	var dot, leftNorm, rightNorm float64
	for i := range left {
		dot += left[i] * right[i]
		leftNorm += left[i] * left[i]
		rightNorm += right[i] * right[i]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func (s *Service) mergeMemoryLinks(fromID, toID string) {
	links, err := s.store.MemoryLinks(fromID)
	if err != nil {
		log.Printf("load memory links for merge %s: %v", fromID, err)
		return
	}
	for _, link := range links {
		id, err := s.store.ID("memory_link")
		if err != nil {
			log.Printf("create merged memory link id: %v", err)
			continue
		}
		_ = s.store.AddMemoryLink(domain.MemoryLink{
			ID:           id,
			MemoryID:     toID,
			RepositoryID: link.RepositoryID,
			TargetType:   link.TargetType,
			TargetID:     link.TargetID,
			Label:        "merged_from:" + fromID,
			CreatedAt:    link.CreatedAt,
		})
	}
}
