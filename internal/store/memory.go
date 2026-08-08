package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"codecodriver/internal/domain"
)

var ErrNotFound = errors.New("not found")
var ErrStaleRun = errors.New("stale run token")

type Memory struct {
	mu                sync.RWMutex
	seq               map[string]int
	repositories      map[string]domain.Repository
	files             map[string][]domain.RepositoryFile
	symbols           map[string][]domain.Symbol
	tasks             map[string]domain.Task
	runs              map[string][]domain.TaskRun
	steps             map[string][]domain.TaskStep
	toolCalls         map[string][]domain.ToolCall
	llmUsages         map[string][]domain.LLMUsage
	artifacts         map[string][]domain.Artifact
	memories          []domain.MemoryEntry
	benchmarkCases    map[string]domain.BenchmarkCase
	evaluationRuns    []domain.EvaluationRun
	evaluationBatches map[string]domain.EvaluationBatch
	metricSnapshots   map[string]domain.EvaluationMetricSnapshot
}

var _ Store = (*Memory)(nil)

func NewMemory() *Memory {
	return &Memory{
		seq:               map[string]int{},
		repositories:      map[string]domain.Repository{},
		files:             map[string][]domain.RepositoryFile{},
		symbols:           map[string][]domain.Symbol{},
		tasks:             map[string]domain.Task{},
		runs:              map[string][]domain.TaskRun{},
		steps:             map[string][]domain.TaskStep{},
		toolCalls:         map[string][]domain.ToolCall{},
		llmUsages:         map[string][]domain.LLMUsage{},
		artifacts:         map[string][]domain.Artifact{},
		benchmarkCases:    map[string]domain.BenchmarkCase{},
		evaluationBatches: map[string]domain.EvaluationBatch{},
		metricSnapshots:   map[string]domain.EvaluationMetricSnapshot{},
	}
}

func (m *Memory) AddBenchmarkCase(item domain.BenchmarkCase) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.benchmarkCases[item.ID] = item
	return nil
}
func (m *Memory) UpdateBenchmarkCase(item domain.BenchmarkCase) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.benchmarkCases[item.ID]; !ok {
		return ErrNotFound
	}
	m.benchmarkCases[item.ID] = item
	return nil
}
func (m *Memory) BenchmarkCases() ([]domain.BenchmarkCase, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.BenchmarkCase, 0, len(m.benchmarkCases))
	for _, item := range m.benchmarkCases {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func (m *Memory) BenchmarkCase(id string) (domain.BenchmarkCase, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.benchmarkCases[id]
	if !ok {
		return item, ErrNotFound
	}
	return item, nil
}
func (m *Memory) AddEvaluationRun(run domain.EvaluationRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evaluationRuns = append(m.evaluationRuns, run)
	return nil
}
func (m *Memory) UpdateEvaluationRun(run domain.EvaluationRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.evaluationRuns {
		if m.evaluationRuns[i].ID == run.ID {
			m.evaluationRuns[i] = run
			return nil
		}
	}
	return ErrNotFound
}
func (m *Memory) EvaluationRuns(caseID string) ([]domain.EvaluationRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []domain.EvaluationRun{}
	for _, run := range m.evaluationRuns {
		if run.CaseID == caseID {
			out = append(out, run)
		}
	}
	return out, nil
}
func (m *Memory) AllEvaluationRuns() ([]domain.EvaluationRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]domain.EvaluationRun(nil), m.evaluationRuns...), nil
}
func (m *Memory) AddEvaluationBatch(batch domain.EvaluationBatch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evaluationBatches[batch.ID] = batch
	return nil
}
func (m *Memory) UpdateEvaluationBatch(batch domain.EvaluationBatch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.evaluationBatches[batch.ID]; !ok {
		return ErrNotFound
	}
	m.evaluationBatches[batch.ID] = batch
	return nil
}
func (m *Memory) EvaluationBatches() ([]domain.EvaluationBatch, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.EvaluationBatch, 0, len(m.evaluationBatches))
	for _, batch := range m.evaluationBatches {
		out = append(out, batch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func (m *Memory) AddEvaluationMetricSnapshot(snapshot domain.EvaluationMetricSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metricSnapshots[snapshot.BatchID] = snapshot
	return nil
}
func (m *Memory) EvaluationMetricSnapshots() ([]domain.EvaluationMetricSnapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.EvaluationMetricSnapshot, 0, len(m.metricSnapshots))
	for _, snapshot := range m.metricSnapshots {
		out = append(out, snapshot)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) Close() error { return nil }

func (m *Memory) ID(prefix string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq[prefix]++
	return fmt.Sprintf("%s-%d", prefix, m.seq[prefix]), nil
}

func (m *Memory) AddRepository(r domain.Repository) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repositories[r.ID] = r
	return nil
}

func (m *Memory) Repository(id string) (domain.Repository, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.repositories[id]
	if !ok {
		return r, ErrNotFound
	}
	return r, nil
}

func (m *Memory) Repositories() ([]domain.Repository, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.Repository, 0, len(m.repositories))
	for _, r := range m.repositories {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) SetIndex(r domain.Repository, files []domain.RepositoryFile, symbols []domain.Symbol) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repositories[r.ID], m.files[r.ID], m.symbols[r.ID] = r, files, symbols
	return nil
}

func (m *Memory) Files(id string) ([]domain.RepositoryFile, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]domain.RepositoryFile(nil), m.files[id]...), nil
}

func (m *Memory) Symbols(id string) ([]domain.Symbol, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]domain.Symbol(nil), m.symbols[id]...), nil
}

func (m *Memory) AddTask(t domain.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[t.ID] = t
	return nil
}
func (m *Memory) Task(id string) (domain.Task, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tasks[id]
	if !ok {
		return t, ErrNotFound
	}
	return t, nil
}
func (m *Memory) Tasks() ([]domain.Task, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func (m *Memory) UpdateTask(id string, status domain.TaskStatus, errText string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tasks[id]
	t.Status, t.Error, t.UpdatedAt = status, errText, time.Now().UTC()
	m.tasks[id] = t
	return nil
}
func (m *Memory) UpdateTaskForRun(id, runID string, token int64, status domain.TaskStatus, errText string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, run := range m.runs[id] {
		if run.ID == runID && run.FencingToken == token {
			t := m.tasks[id]
			t.Status, t.Error, t.UpdatedAt = status, errText, time.Now().UTC()
			m.tasks[id] = t
			return nil
		}
	}
	return ErrStaleRun
}
func (m *Memory) AddRun(r domain.TaskRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[r.TaskID] = append(m.runs[r.TaskID], r)
	return nil
}
func (m *Memory) FinishRun(taskID, runID string, status domain.TaskStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rs := m.runs[taskID]
	for i := range rs {
		if rs[i].ID == runID {
			rs[i].Status, rs[i].EndedAt = status, time.Now().UTC()
		}
	}
	m.runs[taskID] = rs
	return nil
}
func (m *Memory) FinishRunWithToken(taskID, runID string, status domain.TaskStatus, token int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rs := m.runs[taskID]
	found := false
	for i := range rs {
		if rs[i].ID == runID && rs[i].FencingToken == token {
			rs[i].Status, rs[i].EndedAt = status, time.Now().UTC()
			found = true
		}
	}
	m.runs[taskID] = rs
	if !found {
		return ErrStaleRun
	}
	return nil
}
func (m *Memory) Runs(id string) ([]domain.TaskRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]domain.TaskRun(nil), m.runs[id]...), nil
}
func (m *Memory) AddStep(s domain.TaskStep) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steps[s.TaskID] = append(m.steps[s.TaskID], s)
	return nil
}
func (m *Memory) Steps(id string) ([]domain.TaskStep, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]domain.TaskStep(nil), m.steps[id]...), nil
}
func (m *Memory) AddToolCall(call domain.ToolCall) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toolCalls[call.TaskID] = append(m.toolCalls[call.TaskID], call)
	return nil
}
func (m *Memory) ToolCalls(taskID string) ([]domain.ToolCall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]domain.ToolCall(nil), m.toolCalls[taskID]...), nil
}
func (m *Memory) AddLLMUsage(usage domain.LLMUsage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.llmUsages[usage.TaskID] = append(m.llmUsages[usage.TaskID], usage)
	return nil
}
func (m *Memory) LLMUsages(taskID string) ([]domain.LLMUsage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]domain.LLMUsage(nil), m.llmUsages[taskID]...), nil
}
func (m *Memory) AddArtifact(a domain.Artifact) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.artifacts[a.TaskID] = append(m.artifacts[a.TaskID], a)
	return nil
}
func (m *Memory) Artifacts(id string) ([]domain.Artifact, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]domain.Artifact(nil), m.artifacts[id]...), nil
}
func (m *Memory) AddMemory(e domain.MemoryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(e.Embedding) == 0 {
		e.Embedding = textEmbedding(e.Content)
	}
	m.memories = append(m.memories, e)
	return nil
}
func (m *Memory) SearchMemory(repoID, query string) ([]domain.MemoryEntry, error) {
	return m.SearchMemoryLimit(repoID, query, 20)
}

func (m *Memory) SearchMemoryLimit(repoID, query string, limit int) ([]domain.MemoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q, out := strings.ToLower(query), []domain.MemoryEntry{}
	for _, e := range m.memories {
		if e.RepositoryID != repoID {
			continue
		}
		if q == "" {
			e.Score = 0
			out = append(out, e)
			continue
		}
		e.Score = memoryRerankScore(e, q, time.Now().UTC())
		if e.Score > 0 {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	for i := range out {
		for j := range m.memories {
			if m.memories[j].ID == out[i].ID {
				m.memories[j].AccessCount++
				m.memories[j].LastAccessedAt = time.Now().UTC()
				out[i].AccessCount = m.memories[j].AccessCount
				out[i].LastAccessedAt = m.memories[j].LastAccessedAt
			}
		}
	}
	return out, nil
}

func (m *Memory) RecordMemoryAccess(ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	for i := range m.memories {
		for _, id := range ids {
			if m.memories[i].ID == id {
				m.memories[i].AccessCount++
				m.memories[i].LastAccessedAt = now
			}
		}
	}
	return nil
}

func memoryScore(content, query string) float64 {
	if query == "" {
		return 0
	}
	terms := strings.Fields(query)
	matched := 0
	lower := strings.ToLower(content)
	for _, term := range terms {
		if len(term) >= 3 && strings.Contains(lower, strings.ToLower(term)) {
			matched++
		}
	}
	return float64(matched)
}
