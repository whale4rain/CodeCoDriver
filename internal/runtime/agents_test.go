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

func TestCodebaseAgentIncludesExistingTestPair(t *testing.T) {
	root := t.TempDir()
	files := []struct {
		path string
		body string
	}{
		{"internal/healthcheck/api.go", "package healthcheck\n\nfunc healthcheck() {}\n"},
		{"internal/healthcheck/api_test.go", "package healthcheck\n\nfunc TestAPI(t *testing.T) {}\n"},
		{"internal/errors/response.go", "package errors\n\nfunc Response() {}\n"},
		{"pkg/pagination/pages.go", "package pagination\n\nfunc New() {}\n"},
		{".gitignore", "# coverage\n"},
	}
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(file.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repoFiles := []domain.RepositoryFile{
		{RepositoryID: "repo-1", Path: "internal/healthcheck/api.go", Language: "go", Summary: "package healthcheck"},
		{RepositoryID: "repo-1", Path: "internal/healthcheck/api_test.go", Language: "go", Summary: "package healthcheck"},
		{RepositoryID: "repo-1", Path: "internal/errors/response.go", Language: "go", Summary: "package errors"},
		{RepositoryID: "repo-1", Path: "pkg/pagination/pages.go", Language: "go", Summary: "package pagination"},
		{RepositoryID: "repo-1", Path: ".gitignore", Summary: "# coverage"},
	}
	request := AgentRequest{
		Task:       domain.Task{Title: "Harden health endpoint timeout behavior", Description: "Add focused coverage for response contract and timeout-safe behavior."},
		Repository: domain.Repository{ID: "repo-1", Name: "sample", Path: root},
		Files:      repoFiles,
		Context:    map[string]any{},
	}
	result, err := (CodebaseAgent{Retriever: retrieval.New(retrieval.Config{})}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	output, ok := result.Output.(map[string]any)
	if !ok {
		t.Fatalf("output=%T", result.Output)
	}
	filesOut, ok := output["files"].([]string)
	if !ok {
		t.Fatalf("files=%T", output["files"])
	}
	got := strings.Join(filesOut, "\n")
	if !strings.Contains(got, "internal/healthcheck/api.go") || !strings.Contains(got, "internal/healthcheck/api_test.go") {
		t.Fatalf("missing source/test pair: %s", got)
	}
	if strings.Contains(got, ".gitignore") {
		t.Fatalf("irrelevant file selected: %s", got)
	}
}

func TestCodebaseMemoryBoostsHistoricalFiles(t *testing.T) {
	repoFiles := []domain.RepositoryFile{
		{RepositoryID: "repo-1", Path: "internal/healthcheck/api.go", Language: "go", Summary: "package healthcheck"},
		{RepositoryID: "repo-1", Path: "internal/errors/response.go", Language: "go", Summary: "package errors"},
		{RepositoryID: "repo-1", Path: "pkg/pagination/pages.go", Language: "go", Summary: "package pagination"},
		{RepositoryID: "repo-1", Path: "pkg/retry/backoff.go", Language: "go", Summary: "package retry"},
		{RepositoryID: "repo-1", Path: "cmd/api/main.go", Language: "go", Summary: "package main"},
		{RepositoryID: "repo-1", Path: "internal/cache/cache.go", Language: "go", Summary: "package cache"},
	}
	request := AgentRequest{
		Task:       domain.Task{Title: "Unrelated refactor", Description: "Improve internal code structure."},
		Repository: domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir()},
		Files:      repoFiles,
		Context: map[string]any{"memory": []domain.MemoryEntry{{
			Kind:         "execution_success",
			Summary:      "pagination validation completed",
			ChangedFiles: []string{"pkg/pagination/pages.go"},
			Symbols:      []string{"New"},
		}}},
	}
	result, err := (CodebaseAgent{Retriever: retrieval.New(retrieval.Config{})}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	output, ok := result.Output.(map[string]any)
	if !ok {
		t.Fatalf("output=%T", result.Output)
	}
	filesOut, ok := output["files"].([]string)
	if !ok {
		t.Fatalf("files=%T", output["files"])
	}
	if !containsString(filesOut, "pkg/pagination/pages.go") {
		t.Fatalf("memory file not boosted: %v", filesOut)
	}
}

func TestPatchAndReviewerReceiveMemoryGuidance(t *testing.T) {
	fake := &recordingLLM{responses: []string{"--- a/sample.go\n+++ b/sample.go\n@@", "REQUEST_CHANGES"}}
	request := AgentRequest{
		Task:    domain.Task{Title: "fix retry", Description: "fix retry"},
		Context: map[string]any{"memory": []domain.MemoryEntry{{Kind: "failure_pattern", Summary: "retry timeout", Symptom: "timeout", RootCause: "retry too aggressive", ChangedFiles: []string{"sample.go"}}}},
	}
	if _, err := (PatchAgent{LLM: fake}).Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.prompts[0], "failure_pattern") || !strings.Contains(fake.prompts[0], "do not repeat the failed approach") {
		t.Fatalf("patch prompt missing memory guidance: %s", fake.prompts[0])
	}
	request.Context["patch"] = map[string]any{"proposal": "patch"}
	if _, err := (ReviewerAgent{LLM: fake}).Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.prompts[1], "failure_pattern") || !strings.Contains(fake.prompts[1], "does not repeat known failure patterns") {
		t.Fatalf("review prompt missing memory guidance: %s", fake.prompts[1])
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestPatchRepairPromptResetsToOriginalState(t *testing.T) {
	fake := &recordingLLM{responses: []string{"--- a/sample.go\n+++ b/sample.go\n@@ -1 +1 @@\n-old\n+new\n"}}
	request := AgentRequest{
		Task:    domain.Task{Title: "repair", Description: "fix patch"},
		Attempt: 2,
		Context: map[string]any{"repair_feedback": map[string]any{"status": "apply_failed"}},
	}
	if _, err := (PatchAgent{LLM: fake}).Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	prompt := fake.prompts[0]
	if !strings.Contains(prompt, "ORIGINAL repository") || !strings.Contains(prompt, "complete standalone diff") {
		t.Fatalf("repair prompt missing state reset: %s", prompt)
	}
	if !strings.Contains(prompt, "TASK CONTRACT") || !strings.Contains(prompt, "production code") {
		t.Fatalf("repair prompt missing behavior contract: %s", prompt)
	}
	if !strings.Contains(prompt, "/dev/null") || !strings.Contains(prompt, "already exists") || !strings.Contains(prompt, "diff --git") || !strings.Contains(prompt, "unchanged context line") {
		t.Fatalf("repair prompt missing diff rules: %s", prompt)
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
