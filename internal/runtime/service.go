package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/llm"
	"codecodriver/internal/store"
)

type Service struct {
	store   *store.Memory
	indexer *indexer.Indexer
	queue   chan string
	agents  []Agent
}

func NewService(s *store.Memory, idx *indexer.Indexer) *Service {
	return &Service{store: s, indexer: idx, queue: make(chan string, 128), agents: []Agent{PlannerAgent{}, CodebaseAgent{}, PatchAgent{}, TestAgent{}, ReviewerAgent{}}}
}

func NewServiceWithLLM(s *store.Memory, idx *indexer.Indexer, client llm.Client) *Service {
	return &Service{store: s, indexer: idx, queue: make(chan string, 128), agents: []Agent{PlannerAgent{LLM: client}, CodebaseAgent{}, PatchAgent{LLM: client}, TestAgent{}, ReviewerAgent{LLM: client}}}
}

func (s *Service) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case taskID := <-s.queue:
				s.execute(ctx, taskID)
			}
		}
	}()
}

func (s *Service) RegisterRepository(name, path string) (domain.Repository, error) {
	info, err := os.Stat(path)
	if err != nil {
		return domain.Repository{}, err
	}
	if !info.IsDir() {
		return domain.Repository{}, fmt.Errorf("repository path is not a directory")
	}
	now := time.Now().UTC()
	repo := domain.Repository{ID: s.store.ID("repo"), Name: strings.TrimSpace(name), Path: path, CreatedAt: now}
	if repo.Name == "" {
		repo.Name = info.Name()
	}
	s.store.AddRepository(repo)
	return s.IndexRepository(repo.ID)
}

func (s *Service) IndexRepository(id string) (domain.Repository, error) {
	repo, err := s.store.Repository(id)
	if err != nil {
		return repo, err
	}
	repo, files, symbols, err := s.indexer.Index(repo)
	if err != nil {
		return repo, err
	}
	s.store.SetIndex(repo, files, symbols)
	return repo, nil
}

func (s *Service) CreateTask(repoID, title, description string) (domain.Task, error) {
	if _, err := s.store.Repository(repoID); err != nil {
		return domain.Task{}, err
	}
	if strings.TrimSpace(description) == "" {
		return domain.Task{}, fmt.Errorf("description is required")
	}
	now := time.Now().UTC()
	task := domain.Task{ID: s.store.ID("task"), RepositoryID: repoID, Title: strings.TrimSpace(title), Description: strings.TrimSpace(description), Status: domain.TaskCreated, CreatedAt: now, UpdatedAt: now}
	s.store.AddTask(task)
	s.queue <- task.ID
	return task, nil
}

func (s *Service) execute(ctx context.Context, taskID string) {
	task, err := s.store.Task(taskID)
	if err != nil {
		return
	}
	repo, err := s.store.Repository(task.RepositoryID)
	if err != nil {
		s.fail(task, "", err)
		return
	}
	runID := s.store.ID("run")
	s.store.AddRun(domain.TaskRun{ID: runID, TaskID: task.ID, Status: domain.TaskIndexCheck, StartedAt: time.Now().UTC()})
	if repo.IndexedAt.IsZero() {
		s.store.UpdateTask(task.ID, domain.TaskIndexCheck, "")
		repo, err = s.IndexRepository(repo.ID)
		if err != nil {
			s.fail(task, runID, err)
			return
		}
	}
	statuses := []domain.TaskStatus{domain.TaskPlanning, domain.TaskRetrievingContext, domain.TaskGeneratingPatch, domain.TaskRunningTests, domain.TaskReviewing}
	contextData := map[string]any{}
	for i, agent := range s.agents {
		s.store.UpdateTask(task.ID, statuses[i], "")
		started := time.Now().UTC()
		req := AgentRequest{Task: task, Repository: repo, Files: s.store.Files(repo.ID), Symbols: s.store.Symbols(repo.ID), Context: contextData}
		result, runErr := agent.Run(ctx, req)
		ended := time.Now().UTC()
		step := domain.TaskStep{ID: s.store.ID("step"), TaskID: task.ID, RunID: runID, AgentName: agent.Name(), StepType: string(statuses[i]), Status: "COMPLETED", Input: map[string]any{"task": task.Description}, Output: result.Output, StartedAt: started, EndedAt: ended, LatencyMS: ended.Sub(started).Milliseconds()}
		if runErr != nil {
			step.Status, step.Error = "FAILED", runErr.Error()
			s.store.AddStep(step)
			s.fail(task, runID, runErr)
			return
		}
		s.store.AddStep(step)
		contextData[agent.Name()] = result.Output
		if result.ArtifactType != "" {
			s.store.AddArtifact(domain.Artifact{ID: s.store.ID("artifact"), TaskID: task.ID, RunID: runID, Type: result.ArtifactType, Name: result.ArtifactName, Content: result.ArtifactContent, CreatedAt: ended})
		}
	}
	s.store.UpdateTask(task.ID, domain.TaskCompleted, "")
	s.store.FinishRun(task.ID, runID, domain.TaskCompleted)
	s.store.AddMemory(domain.MemoryEntry{ID: s.store.ID("memory"), RepositoryID: repo.ID, TaskID: task.ID, Kind: "execution_summary", Content: fmt.Sprintf("%s: completed plan, retrieval, patch proposal, validation, and review", task.Title), CreatedAt: time.Now().UTC()})
}

func (s *Service) fail(task domain.Task, runID string, err error) {
	s.store.UpdateTask(task.ID, domain.TaskFailed, err.Error())
	if runID != "" {
		s.store.FinishRun(task.ID, runID, domain.TaskFailed)
	}
}
