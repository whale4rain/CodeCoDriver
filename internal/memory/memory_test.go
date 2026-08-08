package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/store"
)

type fakeLLM struct {
	responses []string
	prompts   []string
}

func (f *fakeLLM) Complete(_ context.Context, _, userPrompt string) (string, error) {
	f.prompts = append(f.prompts, userPrompt)
	if len(f.responses) == 0 {
		return "{}", nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func TestRefineCreatesRefinedMemoryAndLink(t *testing.T) {
	data := store.NewMemory()
	now := time.Now()
	raw := domain.MemoryEntry{
		ID: "raw-success", RepositoryID: "repo", TaskID: "task", Kind: "execution_success",
		Title: "fix retry", Summary: "retry fix passed", Symptom: "timeout", RootCause: "retry too aggressive",
		ChangedFiles: []string{"internal/retry.go"}, SuccessScore: 1, CreatedAt: now,
	}
	if err := data.AddMemory(raw); err != nil {
		t.Fatal(err)
	}
	fake := &fakeLLM{responses: []string{`{"title":"fix retry with backoff","summary":"use exponential backoff","symptom":"timeout","root_cause":"fixed interval","changed_files":["internal/retry.go"],"symbols":["Retry"],"condition":"when remote is slow","success_score":0.9}`}}
	service := New(data, fake)
	if err := service.Process(context.Background(), []domain.MemoryEntry{raw}); err != nil {
		t.Fatal(err)
	}
	memories, err := data.SearchMemoryLimit("repo", "retry", 10)
	if err != nil {
		t.Fatal(err)
	}
	var refined *domain.MemoryEntry
	for i := range memories {
		if memories[i].Kind == "refined_execution_success" {
			refined = &memories[i]
		}
	}
	if refined == nil || refined.Summary != "use exponential backoff" || refined.SuccessScore != 0.9 || len(refined.Links) == 0 || refined.Links[0].TargetID != raw.ID {
		t.Fatalf("refined=%+v memories=%+v", refined, memories)
	}
	updated, err := data.GetMemory(raw.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RefinedAt == nil {
		t.Fatalf("raw refined_at not updated: %+v", updated)
	}
}

func TestDedupeMarksNearDuplicate(t *testing.T) {
	data := store.NewMemory()
	now := time.Now()
	first := domain.MemoryEntry{ID: "success-1", RepositoryID: "repo", Kind: "execution_success", Title: "fix retry", Content: "use retry backoff", Summary: "use retry backoff", Symptom: "timeout", RootCause: "fixed interval", ChangedFiles: []string{"retry.go"}, SuccessScore: 1, CreatedAt: now}
	second := domain.MemoryEntry{ID: "success-2", RepositoryID: "repo", Kind: "execution_success", Title: "fix retry", Content: "use retry backoff", Summary: "use retry backoff", Symptom: "timeout", RootCause: "fixed interval", ChangedFiles: []string{"retry.go"}, SuccessScore: 1, CreatedAt: now}
	if err := data.AddMemory(first); err != nil {
		t.Fatal(err)
	}
	if err := data.AddMemory(second); err != nil {
		t.Fatal(err)
	}
	service := New(data, nil)
	if err := service.Process(context.Background(), []domain.MemoryEntry{first, second}); err != nil {
		t.Fatal(err)
	}
	firstGot, err := data.GetMemory(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondGot, err := data.GetMemory(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstGot.DuplicateOf == "" && secondGot.DuplicateOf == "" {
		t.Fatalf("no duplicate marked: first=%+v second=%+v", firstGot, secondGot)
	}
	if firstGot.DuplicateOf != "" && firstGot.DuplicateOf != secondGot.ID {
		t.Fatalf("first duplicate=%q", firstGot.DuplicateOf)
	}
	if secondGot.DuplicateOf != "" && secondGot.DuplicateOf != firstGot.ID {
		t.Fatalf("second duplicate=%q", secondGot.DuplicateOf)
	}
}

func TestConflictCreatesResolvedPattern(t *testing.T) {
	data := store.NewMemory()
	now := time.Now()
	success := domain.MemoryEntry{ID: "conflict-success", RepositoryID: "repo", TaskID: "task", Kind: "execution_success", Title: "fix retry", Summary: "retry with backoff passed", Symptom: "timeout", RootCause: "fixed interval", ChangedFiles: []string{"retry.go"}, SuccessScore: 1, RefinedAt: &now, CreatedAt: now}
	failure := domain.MemoryEntry{ID: "conflict-failure", RepositoryID: "repo", TaskID: "task", Kind: "failure_pattern", Title: "fix retry", Summary: "retry with backoff failed", Symptom: "timeout", RootCause: "fixed interval", ChangedFiles: []string{"retry.go"}, RefinedAt: &now, CreatedAt: now}
	if err := data.AddMemory(success); err != nil {
		t.Fatal(err)
	}
	if err := data.AddMemory(failure); err != nil {
		t.Fatal(err)
	}
	fake := &fakeLLM{responses: []string{`{"condition":"when upstream is down","resolution":"check health first then retry","recommendation":"stop after three attempts","changed_files":["retry.go"]}`}}
	service := New(data, fake)
	if err := service.Process(context.Background(), []domain.MemoryEntry{success, failure}); err != nil {
		t.Fatal(err)
	}
	memories, err := data.SearchMemoryLimit("repo", "retry", 10)
	if err != nil {
		t.Fatal(err)
	}
	var resolved *domain.MemoryEntry
	for i := range memories {
		if memories[i].Kind == "resolved_pattern" {
			resolved = &memories[i]
		}
	}
	if resolved == nil || resolved.ConflictGroupID == "" || resolved.Condition == "" || resolved.Summary != "check health first then retry" {
		t.Fatalf("resolved=%+v memories=%+v", resolved, memories)
	}
	if !hasMemoryTarget(resolved.Links, success.ID) || !hasMemoryTarget(resolved.Links, failure.ID) {
		t.Fatalf("links=%+v", resolved.Links)
	}
	successGot, _ := data.GetMemory(success.ID)
	failureGot, _ := data.GetMemory(failure.ID)
	if successGot.ConflictGroupID == "" || failureGot.ConflictGroupID == "" || successGot.ConflictGroupID != failureGot.ConflictGroupID {
		t.Fatalf("conflict group: success=%+v failure=%+v", successGot, failureGot)
	}
}

func hasMemoryTarget(links []domain.MemoryLink, targetID string) bool {
	for _, link := range links {
		if link.TargetType == "memory" && link.TargetID == targetID {
			return true
		}
	}
	return false
}

func TestParseRefinedMemoryHandlesCodeFence(t *testing.T) {
	content := "Here is the result:\n```json\n{\"summary\":\"use backoff\",\"root_cause\":\"fixed interval\"}\n```"
	parsed, err := parseRefinedMemory(content)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Summary != "use backoff" || !strings.Contains(content, "root_cause") {
		t.Fatalf("parsed=%+v", parsed)
	}
}
