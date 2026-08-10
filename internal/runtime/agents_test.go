package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codecodriver/internal/domain"
	"codecodriver/internal/retrieval"
	"codecodriver/internal/sandbox"
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

	validPatch := "--- a/sample.go\n+++ b/sample.go\n@@ -1,3 +1,3 @@\n package sample\n \n-func Add(a, b int) int { return a + b }\n+func Add(a, b int) int { return a + b }\n"
	fake := &recordingLLM{responses: []string{validPatch, "REQUEST_CHANGES"}}
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

func TestCodebaseIncludesTestHelpersAndSymbolSources(t *testing.T) {
	repoFiles := []domain.RepositoryFile{
		{RepositoryID: "repo-1", Path: "cmd/server/main.go", Language: "go", Summary: "package main"},
		{RepositoryID: "repo-1", Path: "internal/test/mock.go", Language: "go", Summary: "package test"},
		{RepositoryID: "repo-1", Path: "internal/healthcheck/api.go", Language: "go", Summary: "package healthcheck"},
		{RepositoryID: "repo-1", Path: "internal/healthcheck/api_test.go", Language: "go", Summary: "package healthcheck"},
		{RepositoryID: "repo-1", Path: "pkg/pagination/pages.go", Language: "go", Summary: "package pagination"},
	}
	symbols := []domain.Symbol{
		{RepositoryID: "repo-1", FilePath: "cmd/server/main.go", Name: "logDBQuery", Kind: "function", Line: 120},
		{RepositoryID: "repo-1", FilePath: "internal/test/mock.go", Name: "MockRouter", Kind: "function", Line: 1},
	}
	request := AgentRequest{
		Task:       domain.Task{Title: "Cover DB logging paths", Description: "Add focused tests for logDBQuery and logDBExec using test helpers."},
		Repository: domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir()},
		Files:      repoFiles,
		Symbols:    symbols,
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
	if !containsString(filesOut, "cmd/server/main.go") {
		t.Fatalf("symbol source not selected: %v", filesOut)
	}
	if !containsString(filesOut, "internal/test/mock.go") {
		t.Fatalf("test helper not selected: %v", filesOut)
	}
}

func TestPatchAndReviewerReceiveMemoryGuidance(t *testing.T) {
	validPatch := "--- a/sample.go\n+++ b/sample.go\n@@ -1 +1 @@\n-old\n+new\n"
	fake := &recordingLLM{responses: []string{validPatch, "REQUEST_CHANGES"}}
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

func TestPatchAgentRetriesWhenNoDiff(t *testing.T) {
	fake := &recordingLLM{responses: []string{
		"I need to read the source files first.",
		"--- a/sample.go\n+++ b/sample.go\n@@ -1 +1 @@\n-old\n+new\n",
	}}
	request := AgentRequest{
		Task:    domain.Task{Title: "fix", Description: "fix sample"},
		Context: map[string]any{},
	}
	result, err := (PatchAgent{LLM: fake}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.prompts) != 2 {
		t.Fatalf("llm calls=%d, want 2", len(fake.prompts))
	}
	proposal, ok := result.Output.(map[string]any)["proposal"].(string)
	if !ok || !strings.Contains(proposal, "--- a/sample.go") {
		t.Fatalf("proposal=%+v", result.Output)
	}
}

func TestDocumentationTaskClassification(t *testing.T) {
	if !documentationTask(domain.Task{Title: "build a readme", Description: "build a readme for this repo"}) {
		t.Fatal("readme task should be documentation")
	}
	if !documentationTask(domain.Task{Title: "update documentation", Description: "docs"}) {
		t.Fatal("docs task should be documentation")
	}
	if !documentationTask(domain.Task{Title: "中文reamdme", Description: "中文reamdme"}) {
		t.Fatal("chinese readme task should be documentation")
	}
	if documentationTask(domain.Task{Title: "fix retry timeout", Description: "handle retry"}) {
		t.Fatal("code task should not be documentation")
	}
}

func TestCodebaseIncludesReadmeForDocsTask(t *testing.T) {
	repoFiles := []domain.RepositoryFile{
		{RepositoryID: "repo-1", Path: "README.md", Language: "markdown", Summary: "readme"},
		{RepositoryID: "repo-1", Path: "cmd/server/main.go", Language: "go", Summary: "package main"},
		{RepositoryID: "repo-1", Path: "internal/healthcheck/api.go", Language: "go", Summary: "package healthcheck"},
		{RepositoryID: "repo-1", Path: "internal/healthcheck/api_test.go", Language: "go", Summary: "package healthcheck"},
	}
	request := AgentRequest{
		Task:       domain.Task{Title: "build a readme", Description: "build a readme for this repo"},
		Repository: domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir()},
		Files:      repoFiles,
		Context:    map[string]any{},
	}
	result, err := (CodebaseAgent{Retriever: retrieval.New(retrieval.Config{})}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	filesOut, ok := result.Output.(map[string]any)["files"].([]string)
	if !ok {
		t.Fatalf("files=%T", result.Output)
	}
	if !containsString(filesOut, "README.md") {
		t.Fatalf("README not selected: %v", filesOut)
	}
}

func TestPatchAgentDocumentationPrompt(t *testing.T) {
	fake := &recordingLLM{responses: []string{"--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old\n+new\n"}}
	request := AgentRequest{
		Task:    domain.Task{Title: "build a readme", Description: "build a readme for this repo"},
		Context: map[string]any{},
	}
	if _, err := (PatchAgent{LLM: fake}).Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.prompts[0], "DOCUMENTATION TASK") || !strings.Contains(fake.prompts[0], "never create it") {
		t.Fatalf("prompt missing docs contract: %s", fake.prompts[0])
	}
}

func TestTestAgentMarksDocsPassedAfterApply(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := AgentRequest{
		Task:       domain.Task{Title: "build a readme", Description: "build a readme for this repo"},
		Repository: domain.Repository{ID: "repo-docs", Name: "sample", Path: root},
		Context:    map[string]any{"patch": map[string]any{"proposal": "--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old\n+new\n"}},
	}
	result, err := (TestAgent{Sandbox: sandbox.New(sandbox.Config{})}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	report, ok := result.Output.(sandbox.Report)
	if !ok || !report.Passed || !report.Applied {
		t.Fatalf("report=%+v", result.Output)
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
