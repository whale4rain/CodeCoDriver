package memory

import (
	"context"
	"log"
	"strings"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/llm"
	"codecodriver/internal/store"
)

type Service struct {
	store store.Store
	llm   llm.Client
}

func New(store store.Store, client llm.Client) *Service {
	return &Service{store: store, llm: client}
}

// Process refines newly created memories, removes near-duplicates, and turns
// contradictory success/failure pairs into a resolved conditional pattern.
func (s *Service) Process(ctx context.Context, entries []domain.MemoryEntry) error {
	if s == nil || s.store == nil {
		return nil
	}
	s.refineAll(ctx, entries)
	s.dedupeAll(ctx, entries)
	s.resolveConflicts(ctx, entries)
	return nil
}

func (s *Service) refineAll(ctx context.Context, entries []domain.MemoryEntry) {
	if s.llm == nil {
		return
	}
	for _, entry := range entries {
		if !refinable(entry) {
			continue
		}
		if err := s.refineOne(ctx, entry); err != nil {
			log.Printf("refine memory %s: %v", entry.ID, err)
		}
	}
}

func (s *Service) refineOne(ctx context.Context, entry domain.MemoryEntry) error {
	now := time.Now().UTC()
	prompt := "Convert this engineering execution memory into concise, reusable knowledge. Return strict JSON only with fields: title, summary, symptom, root_cause, changed_files, symbols, condition, success_score.\n\n" + memoryEvidence(entry)
	content, err := s.llm.Complete(ctx, "You are a careful memory refiner for a software engineering agent. Preserve evidence; do not invent file paths or symbols.", prompt)
	if err != nil {
		return err
	}
	parsed, err := parseRefinedMemory(content)
	if err != nil {
		return err
	}
	id, err := s.store.ID("memory")
	if err != nil {
		return err
	}
	summary := parsed.Summary
	if strings.TrimSpace(summary) == "" {
		summary = entry.Summary
	}
	title := parsed.Title
	if strings.TrimSpace(title) == "" {
		title = entry.Title
	}
	successScore := parsed.SuccessScore
	if successScore == 0 {
		successScore = entry.SuccessScore
	}
	refined := domain.MemoryEntry{
		ID:                   id,
		RepositoryID:         entry.RepositoryID,
		TaskID:               entry.TaskID,
		Kind:                 "refined_" + entry.Kind,
		Content:              summary,
		Title:                title,
		Summary:              summary,
		Symptom:              firstNonEmpty(parsed.Symptom, entry.Symptom),
		RootCause:            firstNonEmpty(parsed.RootCause, entry.RootCause),
		ChangedFiles:         firstNonEmptySlice(parsed.ChangedFiles, entry.ChangedFiles),
		Symbols:              firstNonEmptySlice(parsed.Symbols, entry.Symbols),
		TestCommand:          entry.TestCommand,
		VerificationEvidence: entry.VerificationEvidence,
		SuccessScore:         successScore,
		SourceRunID:          entry.SourceRunID,
		Condition:            parsed.Condition,
		Source:               "refiner",
		Score:                entry.Score,
		Metadata:             map[string]string{"source_memory_id": entry.ID, "source_kind": entry.Kind},
		CreatedAt:            now,
	}
	if err := s.store.AddMemory(refined); err != nil {
		return err
	}
	if err := s.addMemoryLink(refined, "memory", entry.ID, "refined_from"); err != nil {
		return err
	}
	entry.RefinedAt = &now
	if err := s.store.UpdateMemory(entry); err != nil {
		return err
	}
	return nil
}

func (s *Service) addMemoryLink(memory domain.MemoryEntry, targetType, targetID, label string) error {
	id, err := s.store.ID("memory_link")
	if err != nil {
		return err
	}
	return s.store.AddMemoryLink(domain.MemoryLink{
		ID:           id,
		MemoryID:     memory.ID,
		RepositoryID: memory.RepositoryID,
		TargetType:   targetType,
		TargetID:     targetID,
		Label:        label,
		CreatedAt:    time.Now().UTC(),
	})
}

func refinable(entry domain.MemoryEntry) bool {
	if entry.RefinedAt != nil || entry.DuplicateOf != "" || entry.ConflictGroupID != "" {
		return false
	}
	return entry.Kind == "execution_success" || entry.Kind == "failure_pattern"
}

func memoryEvidence(entry domain.MemoryEntry) string {
	return "title: " + entry.Title + "\nsummary: " + entry.Summary + "\nsymptom: " + entry.Symptom + "\nroot_cause: " + entry.RootCause + "\nfiles: " + strings.Join(entry.ChangedFiles, ",") + "\nsymbols: " + strings.Join(entry.Symbols, ",")
}

func firstNonEmpty(left, right string) string {
	if strings.TrimSpace(left) != "" {
		return left
	}
	return right
}

func firstNonEmptySlice(left, right []string) []string {
	if len(left) > 0 {
		return left
	}
	return right
}
