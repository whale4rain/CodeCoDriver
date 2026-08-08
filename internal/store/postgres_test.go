package store

import (
	"context"
	"os"
	"testing"
	"time"

	"codecodriver/internal/domain"
)

func TestPostgresPersistence(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	data, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if _, err := data.pool.Exec(ctx, "TRUNCATE llm_usages,evaluation_metric_snapshots,evaluation_runs,evaluation_batches,memory_links,memory_entries,artifacts,tool_calls,task_steps,task_runs,tasks,symbols,repository_files,repositories CASCADE"); err != nil {
		t.Fatal(err)
	}
	defer data.pool.Exec(ctx, "TRUNCATE llm_usages,evaluation_metric_snapshots,evaluation_runs,evaluation_batches,memory_links,memory_entries,artifacts,tool_calls,task_steps,task_runs,tasks,symbols,repository_files,repositories CASCADE")

	now := time.Now().UTC().Truncate(time.Microsecond)
	repo := domain.Repository{ID: "repo-test", Name: "sample", Path: "/sample", TestCommand: "go test ./pkg", CreatedAt: now}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	benchmark := domain.BenchmarkCase{ID: "benchmark-test", Name: "smoke", RepositoryID: repo.ID, Title: "persist benchmark", Description: "verify evaluation persistence", Expected: []string{"main.go"}, CreatedAt: now}
	if err := data.AddBenchmarkCase(benchmark); err != nil {
		t.Fatal(err)
	}
	batch := domain.EvaluationBatch{ID: "batch-test", Name: "smoke suite", Mode: "agent", Status: "completed", Total: 1, Completed: 1, Passed: 1, StartedAt: now, EndedAt: now, CreatedAt: now}
	if err := data.AddEvaluationBatch(batch); err != nil {
		t.Fatal(err)
	}
	if err := data.AddEvaluationRun(domain.EvaluationRun{ID: "evaluation-test", CaseID: benchmark.ID, BatchID: batch.ID, Mode: "agent", Status: "completed", Passed: true, DurationMS: 42, StartedAt: now, EndedAt: now, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.AddEvaluationMetricSnapshot(domain.EvaluationMetricSnapshot{ID: "metric-test", BatchID: batch.ID, Mode: "agent", Total: 1, Passed: 1, PassRate: 1, AvgDurationMS: 42, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	repo.PrimaryLanguage, repo.FileCount, repo.IndexedAt = "go", 1, now
	files := []domain.RepositoryFile{{RepositoryID: repo.ID, Path: "main.go", Language: "go", Size: 12, Hash: "hash", Summary: "package main"}}
	symbols := []domain.Symbol{{RepositoryID: repo.ID, FilePath: "main.go", Name: "main", Kind: "function", Line: 3}}
	if err := data.SetIndex(repo, files, symbols); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-test", RepositoryID: repo.ID, Title: "test", Description: "persist execution", Status: domain.TaskCreated, CreatedAt: now, UpdatedAt: now}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	run := domain.TaskRun{ID: "run-test", TaskID: task.ID, Status: domain.TaskPlanning, StartedAt: now}
	if err := data.AddRun(run); err != nil {
		t.Fatal(err)
	}
	step := domain.TaskStep{ID: "step-test", TaskID: task.ID, RunID: run.ID, AgentName: "planner", StepType: "PLANNING", Status: "COMPLETED", Input: map[string]any{"attempt": 1}, Output: map[string]any{"plan": "ok"}, StartedAt: now, EndedAt: now, LatencyMS: 5}
	if err := data.AddStep(step); err != nil {
		t.Fatal(err)
	}
	if err := data.AddToolCall(domain.ToolCall{ID: "tool-test", TaskID: task.ID, RunID: run.ID, StepID: step.ID, ToolName: "parse_document", ProviderType: "gateway", RequestPayload: map[string]any{"filename": "a.txt"}, ResponsePayload: map[string]any{"chunks": 1}, Status: "COMPLETED", StartedAt: now, EndedAt: now, LatencyMS: 4}); err != nil {
		t.Fatal(err)
	}
	if err := data.AddLLMUsage(domain.LLMUsage{ID: "llm-test", TaskID: task.ID, RunID: run.ID, StepID: step.ID, AgentName: "planner", Model: "deepseek-v4-flash", PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14, EstimatedCostUSD: 0.01, LatencyMS: 20, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.AddArtifact(domain.Artifact{ID: "artifact-test", TaskID: task.ID, RunID: run.ID, Type: "plan", Name: "plan.md", Content: "ok", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.AddMemory(domain.MemoryEntry{ID: "memory-test", RepositoryID: repo.ID, TaskID: task.ID, Kind: "summary", Content: "persistent memory", Title: "persist test", Summary: "persistent memory", ChangedFiles: []string{"main.go"}, Symbols: []string{"main"}, TestCommand: repo.TestCommand, SuccessScore: 1, SourceRunID: run.ID, Source: "reviewer", Score: 2, Metadata: map[string]string{"decision": "approve"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.UpdateTask(task.ID, domain.TaskCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if err := data.FinishRun(task.ID, run.ID, domain.TaskCompleted); err != nil {
		t.Fatal(err)
	}

	if got, err := data.Repository(repo.ID); err != nil || got.FileCount != 1 || got.TestCommand != repo.TestCommand {
		t.Fatalf("repository=%+v err=%v", got, err)
	}
	if got, err := data.Files(repo.ID); err != nil || len(got) != 1 {
		t.Fatalf("files=%+v err=%v", got, err)
	}
	if got, err := data.Symbols(repo.ID); err != nil || len(got) != 1 {
		t.Fatalf("symbols=%+v err=%v", got, err)
	}
	if got, err := data.Steps(task.ID); err != nil || len(got) != 1 {
		t.Fatalf("steps=%+v err=%v", got, err)
	}
	if got, err := data.ToolCalls(task.ID); err != nil || len(got) != 1 || got[0].ToolName != "parse_document" {
		t.Fatalf("tool_calls=%+v err=%v", got, err)
	}
	if got, err := data.LLMUsages(task.ID); err != nil || len(got) != 1 || got[0].TotalTokens != 14 {
		t.Fatalf("llm_usages=%+v err=%v", got, err)
	}
	if got, err := data.BenchmarkCases(); err != nil || len(got) != 1 || got[0].Expected[0] != "main.go" {
		t.Fatalf("benchmark_cases=%+v err=%v", got, err)
	}
	if got, err := data.AllEvaluationRuns(); err != nil || len(got) != 1 || !got[0].Passed {
		t.Fatalf("evaluation_runs=%+v err=%v", got, err)
	}
	if got, err := data.EvaluationBatches(); err != nil || len(got) != 1 || got[0].Completed != 1 {
		t.Fatalf("evaluation_batches=%+v err=%v", got, err)
	}
	if got, err := data.EvaluationMetricSnapshots(); err != nil || len(got) != 1 || got[0].PassRate != 1 {
		t.Fatalf("metric_snapshots=%+v err=%v", got, err)
	}
	if got, err := data.Artifacts(task.ID); err != nil || len(got) != 1 {
		t.Fatalf("artifacts=%+v err=%v", got, err)
	}
	if err := data.AddMemoryLink(domain.MemoryLink{ID: "link-test", MemoryID: "memory-test", RepositoryID: repo.ID, TargetType: "file", TargetID: "main.go", Label: "changed_file", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	fetched, err := data.GetMemory("memory-test")
	if err != nil {
		t.Fatal(err)
	}
	fetched.DuplicateOf = "primary-test"
	fetched.ConflictGroupID = "conflict-test"
	fetched.Condition = "when timeout"
	refinedAt := now.Add(time.Second)
	fetched.RefinedAt = &refinedAt
	if err := data.UpdateMemory(fetched); err != nil {
		t.Fatal(err)
	}
	if got, err := data.SearchMemory(repo.ID, "persistent"); err != nil || len(got) != 1 || got[0].Source != "reviewer" || got[0].Metadata["decision"] != "approve" || got[0].Title != "persist test" || len(got[0].ChangedFiles) != 1 || got[0].SuccessScore != 1 || got[0].SourceRunID != run.ID || got[0].DuplicateOf != "primary-test" || got[0].ConflictGroupID != "conflict-test" || got[0].Condition != "when timeout" || got[0].RefinedAt == nil || len(got[0].Links) != 1 || got[0].Links[0].TargetType != "file" || got[0].Links[0].TargetID != "main.go" {
		t.Fatalf("memory=%+v err=%v", got, err)
	}
}

type testVectorProvider struct{}

func (testVectorProvider) Embed(_ context.Context, texts []string) ([][]float64, error) {
	vectors := make([][]float64, len(texts))
	for i, text := range texts {
		vector := make([]float64, doubaoEmbeddingDimensions)
		for _, b := range []byte(text) {
			vector[int(b)%doubaoEmbeddingDimensions]++
		}
		normalizeEmbedding(vector)
		vectors[i] = vector
	}
	return vectors, nil
}

func (testVectorProvider) Name() string { return "test-vector" }
func (testVectorProvider) Dimensions() int {
	return doubaoEmbeddingDimensions
}

func TestPostgresVectorMemory(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	data, err := OpenPostgresWithEmbedding(ctx, databaseURL, testVectorProvider{})
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if _, err := data.pool.Exec(ctx, "TRUNCATE memory_links,memory_entries,repositories CASCADE"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo := domain.Repository{ID: "repo-vector", Name: "vector", Path: "/vector", CreatedAt: now}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	if err := data.AddMemory(domain.MemoryEntry{ID: "memory-vector-1", RepositoryID: repo.ID, Kind: "failure", Content: "request deadline exceeded during retry backoff", Source: "sandbox", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.AddMemory(domain.MemoryEntry{ID: "memory-vector-2", RepositoryID: repo.ID, Kind: "summary", Content: "database schema migration", Source: "runtime", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	results, err := data.SearchMemoryLimit(repo.ID, "retry deadline", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].ID != "memory-vector-1" {
		t.Fatalf("results=%+v", results)
	}
	var vectorCount int
	if err := data.pool.QueryRow(ctx, "SELECT COUNT(*) FROM memory_entries WHERE embedding_halfvec IS NOT NULL").Scan(&vectorCount); err != nil {
		t.Fatal(err)
	}
	if vectorCount != 2 {
		t.Fatalf("vector_count=%d", vectorCount)
	}
}

func TestPostgresDoubaoMemoryFromEnv(t *testing.T) {
	if os.Getenv("DOUBAO_API_KEY") == "" && os.Getenv("CODECODRIVER_EMBEDDING_API_KEY") == "" {
		t.Skip("Doubao embedding API key is not set")
	}
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	data, err := OpenPostgresWithEmbedding(ctx, databaseURL, NewEmbeddingProviderFromEnv())
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if _, err := data.pool.Exec(ctx, "TRUNCATE memory_links,memory_entries,repositories CASCADE"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo := domain.Repository{ID: "repo-doubao", Name: "doubao", Path: "/doubao", CreatedAt: now}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	memory := domain.MemoryEntry{ID: "memory-doubao", RepositoryID: repo.ID, Kind: "failure", Content: "request deadline exceeded during retry backoff", Source: "sandbox", CreatedAt: now}
	if err := data.AddMemory(memory); err != nil {
		t.Fatal(err)
	}
	results, err := data.SearchMemoryLimit(repo.ID, "retry deadline", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].ID != memory.ID || results[0].Score <= 0 {
		t.Fatalf("results=%+v", results)
	}
	var vectorCount int
	if err := data.pool.QueryRow(ctx, "SELECT COUNT(*) FROM memory_entries WHERE embedding_halfvec IS NOT NULL").Scan(&vectorCount); err != nil {
		t.Fatal(err)
	}
	if vectorCount != 1 {
		t.Fatalf("vector_count=%d", vectorCount)
	}
}
