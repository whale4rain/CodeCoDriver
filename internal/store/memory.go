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

type Memory struct {
	mu           sync.RWMutex
	seq          map[string]int
	repositories map[string]domain.Repository
	files        map[string][]domain.RepositoryFile
	symbols      map[string][]domain.Symbol
	tasks        map[string]domain.Task
	runs         map[string][]domain.TaskRun
	steps        map[string][]domain.TaskStep
	artifacts    map[string][]domain.Artifact
	memories     []domain.MemoryEntry
}

var _ Store = (*Memory)(nil)

func NewMemory() *Memory {
	return &Memory{
		seq:          map[string]int{},
		repositories: map[string]domain.Repository{},
		files:        map[string][]domain.RepositoryFile{},
		symbols:      map[string][]domain.Symbol{},
		tasks:        map[string]domain.Task{},
		runs:         map[string][]domain.TaskRun{},
		steps:        map[string][]domain.TaskStep{},
		artifacts:    map[string][]domain.Artifact{},
	}
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
	m.memories = append(m.memories, e)
	return nil
}
func (m *Memory) SearchMemory(repoID, query string) ([]domain.MemoryEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	q, out := strings.ToLower(query), []domain.MemoryEntry{}
	for _, e := range m.memories {
		if e.RepositoryID == repoID && (q == "" || strings.Contains(strings.ToLower(e.Content), q)) {
			out = append(out, e)
		}
	}
	return out, nil
}
