package runtime

import (
	"strings"
	"testing"

	"codecodriver/internal/domain"
)

func TestLeanContextJSONDropsMemoryAndSourcePayloads(t *testing.T) {
	context := map[string]any{
		"memory_candidates": []domain.MemoryEntry{{ID: "candidate", VerificationEvidence: strings.Repeat("candidate", 1000)}},
		"memory":            []domain.MemoryEntry{{ID: "selected", VerificationEvidence: strings.Repeat("selected", 1000)}},
		"codebase": map[string]any{
			"files":             []string{"pkg/pagination/pages.go"},
			"context_pack":      map[string]any{"snippets": []any{map[string]any{"content": strings.Repeat("source", 1000)}}},
			"context_pack_text": strings.Repeat("source", 1000),
		},
	}
	encoded, err := leanContextJSON(context)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, "memory_candidates") || strings.Contains(encoded, "verification_evidence") || strings.Contains(encoded, "context_pack_text") {
		t.Fatalf("lean context still contains heavy payloads: %s", encoded[:minInt(200, len(encoded))])
	}
	if !strings.Contains(encoded, "memory_guidance") {
		t.Fatalf("lean context missing memory guidance: %s", encoded[:minInt(200, len(encoded))])
	}
}

func minInt(left, right int) int {
	if right < left {
		return right
	}
	return left
}
