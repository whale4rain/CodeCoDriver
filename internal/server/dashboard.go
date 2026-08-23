package server

import (
	"net/http"
	"sort"
	"time"

	"codecodriver/internal/domain"
)

type timelineEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Label     string    `json:"label"`
	Status    string    `json:"status,omitempty"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	Payload   any       `json:"payload,omitempty"`
}

func (s *Server) dashboardOverview(w http.ResponseWriter, _ *http.Request) {
	repositories, err := s.store.Repositories()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	tasks, err := s.store.Tasks()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	statusCounts := map[string]int{}
	var totalLatency int64
	var completedRuns int
	for _, task := range tasks {
		statusCounts[string(task.Status)]++
		runs, runErr := s.store.Runs(task.ID)
		if runErr != nil {
			problem(w, http.StatusInternalServerError, runErr)
			return
		}
		for _, run := range runs {
			if run.EndedAt.After(run.StartedAt) {
				totalLatency += run.EndedAt.Sub(run.StartedAt).Milliseconds()
				completedRuns++
			}
		}
	}
	averageLatency := int64(0)
	if completedRuns > 0 {
		averageLatency = totalLatency / int64(completedRuns)
	}
	write(w, http.StatusOK, map[string]any{
		"repositories":           len(repositories),
		"tasks":                  len(tasks),
		"status_counts":          statusCounts,
		"completed":              statusCounts[string(domain.TaskCompleted)],
		"failed":                 statusCounts[string(domain.TaskFailed)],
		"human_review":           statusCounts[string(domain.TaskHumanReview)],
		"active":                 len(tasks) - statusCounts[string(domain.TaskCompleted)] - statusCounts[string(domain.TaskFailed)] - statusCounts[string(domain.TaskCancelled)],
		"average_run_latency_ms": averageLatency,
	})
}

func (s *Server) repositoryOverview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	repo, err := s.store.Repository(id)
	if err != nil {
		problem(w, http.StatusNotFound, err)
		return
	}
	files, err := s.store.Files(id)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	symbols, err := s.store.Symbols(id)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	tasks, err := s.store.Tasks()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	recent := make([]domain.Task, 0, 10)
	for _, task := range tasks {
		if task.RepositoryID == id {
			recent = append(recent, task)
		}
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].UpdatedAt.After(recent[j].UpdatedAt) })
	if len(recent) > 10 {
		recent = recent[:10]
	}
	write(w, http.StatusOK, map[string]any{"repository": repo, "file_count": len(files), "symbol_count": len(symbols), "recent_tasks": recent})
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.Task(id); err != nil {
		problem(w, http.StatusNotFound, err)
		return
	}
	steps, err := s.store.Steps(id)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	toolCalls, err := s.store.ToolCalls(id)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	llmUsages, err := s.store.LLMUsages(id)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	artifacts, err := s.store.Artifacts(id)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	events := make([]timelineEvent, 0, len(steps)+len(toolCalls)+len(llmUsages)+len(artifacts))
	for _, step := range steps {
		events = append(events, timelineEvent{ID: step.ID, Type: "step", Label: step.AgentName, Status: step.Status, Error: step.Error, StartedAt: step.StartedAt, EndedAt: step.EndedAt, LatencyMS: step.LatencyMS, Payload: step.Output})
	}
	for _, call := range toolCalls {
		events = append(events, timelineEvent{ID: call.ID, Type: "tool_call", Label: call.ToolName, Status: call.Status, Error: call.Error, StartedAt: call.StartedAt, EndedAt: call.EndedAt, LatencyMS: call.LatencyMS, Payload: map[string]any{"request": call.RequestPayload, "response": call.ResponsePayload}})
	}
	for _, usage := range llmUsages {
		events = append(events, timelineEvent{ID: usage.ID, Type: "llm_usage", Label: usage.AgentName, Status: usage.Model, StartedAt: usage.CreatedAt, LatencyMS: usage.LatencyMS, Payload: map[string]any{"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens, "total_tokens": usage.TotalTokens, "estimated_cost_usd": usage.EstimatedCostUSD}})
	}
	for _, artifact := range artifacts {
		events = append(events, timelineEvent{ID: artifact.ID, Type: "artifact", Label: artifact.Name, StartedAt: artifact.CreatedAt, Payload: map[string]any{"type": artifact.Type, "content": artifact.Content}})
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].StartedAt.Before(events[j].StartedAt) })
	write(w, http.StatusOK, map[string]any{"task_id": id, "events": events})
}
