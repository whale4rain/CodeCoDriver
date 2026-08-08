package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"codecodriver/internal/domain"
)

const conflictThreshold = 0.5

type conflictResolution struct {
	Condition      string   `json:"condition"`
	Resolution     string   `json:"resolution"`
	Recommendation string   `json:"recommendation"`
	ChangedFiles   []string `json:"changed_files"`
	Symbols        []string `json:"symbols"`
}

func (s *Service) resolveConflicts(ctx context.Context, entries []domain.MemoryEntry) {
	if s.llm == nil {
		return
	}
	resolved := map[string]bool{}
	for _, original := range entries {
		success, err := s.store.GetMemory(original.ID)
		if err != nil {
			log.Printf("load memory for conflict %s: %v", original.ID, err)
			continue
		}
		if !conflictCandidate(success, "execution_success") {
			continue
		}
		candidates, err := s.store.SearchMemoryLimit(success.RepositoryID, conflictQuery(success), 10)
		if err != nil {
			log.Printf("search conflict candidates for %s: %v", success.ID, err)
			continue
		}
		for _, failure := range candidates {
			if !conflictCandidate(failure, "failure_pattern") {
				continue
			}
			pair := pairKey(success.ID, failure.ID)
			if resolved[pair] || similarityScore(success, failure) < conflictThreshold {
				continue
			}
			if err := s.resolveConflict(ctx, success, failure); err != nil {
				log.Printf("resolve conflict %s/%s: %v", success.ID, failure.ID, err)
				continue
			}
			resolved[pair] = true
		}
	}
}

func conflictCandidate(entry domain.MemoryEntry, kind string) bool {
	return entry.Kind == kind && entry.DuplicateOf == "" && entry.ConflictGroupID == ""
}

func conflictQuery(entry domain.MemoryEntry) string {
	return strings.TrimSpace(entry.Title + " " + entry.Symptom + " " + entry.RootCause)
}

func pairKey(left, right string) string {
	if left < right {
		return left + "|" + right
	}
	return right + "|" + left
}

func (s *Service) resolveConflict(ctx context.Context, success, failure domain.MemoryEntry) error {
	now := time.Now().UTC()
	prompt := fmt.Sprintf("Success memory:\n%s\n\nFailure memory:\n%s\n\nExplain when the failure occurs, which resolution is validated, and what to recommend. Return strict JSON only with fields: condition, resolution, recommendation, changed_files, symbols.", memoryEvidence(success), memoryEvidence(failure))
	content, err := s.llm.Complete(ctx, "You are a conflict resolver for engineering agent memory. Do not delete either side; synthesize a conditional pattern.", prompt)
	if err != nil {
		return err
	}
	var parsed conflictResolution
	raw := extractJSON(content)
	if raw == "" {
		return fmt.Errorf("conflict resolver returned no JSON object")
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return fmt.Errorf("parse conflict resolver JSON: %w", err)
	}
	if strings.TrimSpace(parsed.Resolution) == "" {
		return fmt.Errorf("conflict resolver JSON missing resolution")
	}
	groupID, err := s.store.ID("conflict")
	if err != nil {
		return err
	}
	changedFiles := firstNonEmptySlice(parsed.ChangedFiles, append(success.ChangedFiles, failure.ChangedFiles...))
	success.TaskID = firstNonEmpty(success.TaskID, failure.TaskID)
	resolved := domain.MemoryEntry{
		ID:                   groupID,
		RepositoryID:         success.RepositoryID,
		TaskID:               success.TaskID,
		Kind:                 "resolved_pattern",
		Content:              parsed.Resolution,
		Title:                success.Title,
		Summary:              parsed.Resolution,
		Symptom:              success.Symptom,
		RootCause:            failure.RootCause,
		ChangedFiles:         changedFiles,
		Symbols:              parsed.Symbols,
		TestCommand:          success.TestCommand,
		VerificationEvidence: "success_memory=" + success.ID + "; failure_memory=" + failure.ID,
		SuccessScore:         1,
		SourceRunID:          success.SourceRunID,
		Condition:            parsed.Condition,
		ConflictGroupID:      groupID,
		Source:               "conflict_resolver",
		Score:                4,
		Metadata:             map[string]string{"success_memory_id": success.ID, "failure_memory_id": failure.ID, "conflict_group_id": groupID},
		CreatedAt:            now,
	}
	if err := s.store.AddMemory(resolved); err != nil {
		return err
	}
	if err := s.addMemoryLink(resolved, "memory", success.ID, "success_source"); err != nil {
		return err
	}
	if err := s.addMemoryLink(resolved, "memory", failure.ID, "failure_source"); err != nil {
		return err
	}
	success.ConflictGroupID = groupID
	failure.ConflictGroupID = groupID
	if err := s.store.UpdateMemory(success); err != nil {
		return err
	}
	return s.store.UpdateMemory(failure)
}
