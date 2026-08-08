package store

import "codecodriver/internal/domain"

type Store interface {
	Close() error
	ID(prefix string) (string, error)
	AddRepository(domain.Repository) error
	Repository(string) (domain.Repository, error)
	Repositories() ([]domain.Repository, error)
	SetIndex(domain.Repository, []domain.RepositoryFile, []domain.Symbol) error
	Files(string) ([]domain.RepositoryFile, error)
	Symbols(string) ([]domain.Symbol, error)
	AddTask(domain.Task) error
	Task(string) (domain.Task, error)
	Tasks() ([]domain.Task, error)
	UpdateTask(string, domain.TaskStatus, string) error
	AddRun(domain.TaskRun) error
	FinishRun(string, string, domain.TaskStatus) error
	Runs(string) ([]domain.TaskRun, error)
	AddStep(domain.TaskStep) error
	Steps(string) ([]domain.TaskStep, error)
	AddToolCall(domain.ToolCall) error
	ToolCalls(string) ([]domain.ToolCall, error)
	AddLLMUsage(domain.LLMUsage) error
	LLMUsages(string) ([]domain.LLMUsage, error)
	AddArtifact(domain.Artifact) error
	Artifacts(string) ([]domain.Artifact, error)
	AddMemory(domain.MemoryEntry) error
	SearchMemory(string, string) ([]domain.MemoryEntry, error)
	SearchMemoryLimit(string, string, int) ([]domain.MemoryEntry, error)
	RecordMemoryAccess([]string) error
	AddBenchmarkCase(domain.BenchmarkCase) error
	UpdateBenchmarkCase(domain.BenchmarkCase) error
	BenchmarkCases() ([]domain.BenchmarkCase, error)
	BenchmarkCase(string) (domain.BenchmarkCase, error)
	AddEvaluationRun(domain.EvaluationRun) error
	UpdateEvaluationRun(domain.EvaluationRun) error
	EvaluationRuns(string) ([]domain.EvaluationRun, error)
	AllEvaluationRuns() ([]domain.EvaluationRun, error)
	AddEvaluationBatch(domain.EvaluationBatch) error
	UpdateEvaluationBatch(domain.EvaluationBatch) error
	EvaluationBatches() ([]domain.EvaluationBatch, error)
	AddEvaluationMetricSnapshot(domain.EvaluationMetricSnapshot) error
	EvaluationMetricSnapshots() ([]domain.EvaluationMetricSnapshot, error)
}
