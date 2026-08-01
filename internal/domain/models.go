package domain

import "time"

type TaskStatus string

const (
	TaskCreated           TaskStatus = "CREATED"
	TaskIndexCheck        TaskStatus = "INDEX_CHECK"
	TaskPlanning          TaskStatus = "PLANNING"
	TaskRetrievingContext TaskStatus = "RETRIEVING_CONTEXT"
	TaskGeneratingPatch   TaskStatus = "GENERATING_PATCH"
	TaskRunningTests      TaskStatus = "RUNNING_TESTS"
	TaskReviewing         TaskStatus = "REVIEWING"
	TaskReplanRequired    TaskStatus = "REPLAN_REQUIRED"
	TaskHumanReview       TaskStatus = "HUMAN_REVIEW_REQUIRED"
	TaskCancelled         TaskStatus = "CANCELLED"
	TaskCompleted         TaskStatus = "COMPLETED"
	TaskFailed            TaskStatus = "FAILED"
)

type Repository struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Path            string    `json:"path"`
	PrimaryLanguage string    `json:"primary_language,omitempty"`
	FileCount       int       `json:"file_count"`
	IndexedAt       time.Time `json:"indexed_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type RepositoryFile struct {
	RepositoryID string `json:"repository_id"`
	Path         string `json:"path"`
	Language     string `json:"language,omitempty"`
	Size         int64  `json:"size"`
	Hash         string `json:"hash"`
	Summary      string `json:"summary,omitempty"`
}

type Symbol struct {
	RepositoryID string `json:"repository_id"`
	FilePath     string `json:"file_path"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Line         int    `json:"line"`
}

type Task struct {
	ID           string     `json:"id"`
	RepositoryID string     `json:"repository_id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Status       TaskStatus `json:"status"`
	Error        string     `json:"error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type TaskRun struct {
	ID        string     `json:"id"`
	TaskID    string     `json:"task_id"`
	Status    TaskStatus `json:"status"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   time.Time  `json:"ended_at,omitempty"`
}

type TaskStep struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	RunID     string    `json:"run_id"`
	AgentName string    `json:"agent_name"`
	StepType  string    `json:"step_type"`
	Status    string    `json:"status"`
	Input     any       `json:"input,omitempty"`
	Output    any       `json:"output,omitempty"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	LatencyMS int64     `json:"latency_ms"`
}

type Artifact struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	RunID     string    `json:"run_id"`
	Type      string    `json:"type"`
	Name      string    `json:"name"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type MemoryEntry struct {
	ID           string    `json:"id"`
	RepositoryID string    `json:"repository_id"`
	TaskID       string    `json:"task_id,omitempty"`
	Kind         string    `json:"kind"`
	Content      string    `json:"content"`
	CreatedAt    time.Time `json:"created_at"`
}
