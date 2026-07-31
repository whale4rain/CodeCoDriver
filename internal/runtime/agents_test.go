package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codecodriver/internal/domain"
	"codecodriver/internal/retrieval"
)

type recordingLLM struct {
	prompts   []string
	responses []string
}

func (f *recordingLLM) Complete(_ context.Context, _, userPrompt string) (string, error) {
	f.prompts = append(f.prompts, userPrompt)
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func TestPatchAndReviewerReceiveSourceAndProposal(t *testing.T) {
	root := t.TempDir()
	source := "package sample\n\nfunc Add(a, b int) int { return a + b }\n"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	request := AgentRequest{
		Task:       domain.Task{Title: "Improve Add", Description: "Add overflow validation"},
		Repository: domain.Repository{ID: "repo-1", Name: "sample", Path: root},
		Files:      []domain.RepositoryFile{{RepositoryID: "repo-1", Path: "sample.go", Language: "go", Summary: "package sample"}},
		Context:    map[string]any{},
	}
	codebase, err := (CodebaseAgent{Retriever: retrieval.New(retrieval.Config{})}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Context["codebase"] = codebase.Output

	fake := &recordingLLM{responses: []string{"--- a/sample.go\n+++ b/sample.go\n@@", "REQUEST_CHANGES"}}
	patch, err := (PatchAgent{LLM: fake}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.prompts[0], "func Add(a, b int)") {
		t.Fatalf("patch prompt missing source: %s", fake.prompts[0])
	}
	request.Context["patch"] = patch.Output
	if _, err := (ReviewerAgent{LLM: fake}).Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.prompts[1], "--- a/sample.go") {
		t.Fatalf("review prompt missing proposal: %s", fake.prompts[1])
	}
}

func TestParseReviewDecisionUsesFinalDecision(t *testing.T) {
	content := "Consider APPROVE_PROPOSAL or HUMAN_REVIEW_REQUIRED.\n\nDecision: REQUEST_CHANGES"
	if got := parseReviewDecision(content); got != ReviewRequestChanges {
		t.Fatalf("decision=%s", got)
	}
	if got := parseReviewDecision("no explicit decision"); got != ReviewHumanRequired {
		t.Fatalf("decision=%s", got)
	}
}
