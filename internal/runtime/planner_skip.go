package runtime

import (
	"encoding/json"
	"strings"

	"codecodriver/internal/domain"
)

const (
	plannerSkipArtifactType     = "planner_skip"
	plannerSkipDecision         = "SKIP_SUGGESTED"
	plannerContinueDecision     = "continue"
	minPlannerSkipEvidenceScore = 4.5
)

type plannerSkipEvidence struct {
	MemoryID     string   `json:"memory_id,omitempty"`
	SourceTaskID string   `json:"source_task_id,omitempty"`
	MemoryTitle  string   `json:"memory_title,omitempty"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	Score        float64  `json:"score"`
	Reason       string   `json:"reason"`
}

func suggestPlannerSkip(r AgentRequest) (map[string]any, bool) {
	if hasPlannerArtifactDecision(r.Artifacts, plannerContinueDecision) {
		return nil, false
	}
	memories := plannerMemoryCandidates(r)
	if len(memories) == 0 {
		return nil, false
	}
	best := plannerSkipEvidence{}
	bestScore := 0.0
	for _, memory := range memories {
		if !plannerSuccessMemory(memory.Kind) || memory.DuplicateOf != "" || memory.ConflictGroupID != "" {
			continue
		}
		score, reason := plannerDuplicateScore(memory, r.Task, r.Files)
		if score > bestScore {
			bestScore = score
			best = plannerSkipEvidence{
				MemoryID:     memory.ID,
				SourceTaskID: memory.TaskID,
				MemoryTitle:  memory.Title,
				ChangedFiles: append([]string(nil), memory.ChangedFiles...),
				Score:        score,
				Reason:       reason,
			}
		}
	}
	if bestScore < minPlannerSkipEvidenceScore {
		return nil, false
	}
	reason := best.Reason
	if reason == "" {
		reason = "historical memory and current file tree indicate the requested work was already completed"
	}
	return map[string]any{
		"decision": PlannerSkipDecision,
		"reason":   reason,
		"evidence": best,
		"plan":     "Suggested skip: the repository already contains the deliverable from a prior successful execution. Waiting for human confirmation before ending the task.",
	}, true
}

func plannerMemoryCandidates(r AgentRequest) []domain.MemoryEntry {
	if candidates, ok := r.Context["memory_candidates"].([]domain.MemoryEntry); ok {
		return candidates
	}
	if memories, ok := r.Context["memory"].([]domain.MemoryEntry); ok {
		return memories
	}
	return nil
}

func plannerSuccessMemory(kind string) bool {
	switch kind {
	case "execution_success", "resolved_pattern", "refined_execution_success":
		return true
	default:
		return false
	}
}

func plannerDuplicateScore(memory domain.MemoryEntry, task domain.Task, files []domain.RepositoryFile) (float64, string) {
	query := normalizePlannerText(task.Title + " " + task.Description)
	text := normalizePlannerText(strings.Join([]string{
		memory.Title,
		memory.Summary,
		memory.Content,
		memory.Condition,
		memory.VerificationEvidence,
	}, " "))
	score := 0.0
	reasons := []string{}
	if text != "" && (strings.Contains(text, query) || strings.Contains(query, text)) {
		score += 3
		reasons = append(reasons, "phrase overlap")
	}
	if memory.Title != "" && normalizePlannerText(memory.Title) == normalizePlannerText(task.Title) {
		score += 2
		reasons = append(reasons, "same task title")
	}
	if overlap := plannerTokenOverlap(query, text); overlap >= 2 {
		score += 1.5
		reasons = append(reasons, "strong keyword overlap")
	}
	if memory.SuccessScore > 0 {
		score++
		reasons = append(reasons, "verified success memory")
	}
	if memory.Score > 0 {
		score += minFloat(1.5, memory.Score*0.2)
	}
	if documentationTask(task) {
		if target := plannerExistingDocTarget(memory, files); target != "" {
			score += 3
			reasons = append(reasons, "target documentation file already exists")
		}
	}
	return score, strings.Join(reasons, "; ")
}

func plannerExistingDocTarget(memory domain.MemoryEntry, files []domain.RepositoryFile) string {
	memoryFiles := map[string]bool{}
	for _, path := range memory.ChangedFiles {
		memoryFiles[normalizePlannerPath(path)] = true
	}
	for _, file := range files {
		if !isDocumentationFile(file.Path) {
			continue
		}
		if memoryFiles[normalizePlannerPath(file.Path)] {
			return file.Path
		}
	}
	return ""
}

func plannerTokenOverlap(left, right string) int {
	leftTokens := map[string]bool{}
	for _, token := range runtimeMemoryTokens(left) {
		leftTokens[token] = true
	}
	seen := map[string]bool{}
	count := 0
	for _, token := range runtimeMemoryTokens(right) {
		if leftTokens[token] && !seen[token] {
			seen[token] = true
			count++
		}
	}
	return count
}

func normalizePlannerText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func normalizePlannerPath(path string) string {
	return strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
}

func minFloat(left, right float64) float64 {
	if right < left {
		return right
	}
	return left
}

func hasPlannerArtifactDecision(artifacts []domain.Artifact, decision string) bool {
	for _, artifact := range artifacts {
		if artifact.Type != plannerSkipArtifactType {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(artifact.Content), &payload); err != nil {
			continue
		}
		if value, ok := payload["decision"].(string); ok && value == decision {
			return true
		}
	}
	return false
}

func plannerDecisionFromResult(output any) string {
	result, ok := output.(map[string]any)
	if !ok {
		return ""
	}
	decision, _ := result["decision"].(string)
	return decision
}

func plannerSkipReason(output any) string {
	result, ok := output.(map[string]any)
	if !ok {
		return "task appears to already be completed"
	}
	reason, _ := result["reason"].(string)
	if reason == "" {
		reason = "task appears to already be completed"
	}
	return truncateFeedback(reason)
}
