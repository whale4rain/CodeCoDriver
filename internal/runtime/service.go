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
	"codecodriver/internal/retrieval"
	"codecodriver/internal/sandbox"
	"codecodriver/internal/store"
)

type Service struct {
	store    store.Store
	indexer  *indexer.Indexer
	queue    chan string
	planner  Agent
	codebase Agent
	patch    Agent
	test     Agent
	reviewer Agent
}

const maxPatchAttempts = 3
const maxRepairFeedbackBytes = 8 * 1024

func NewService(s store.Store, idx *indexer.Indexer) *Service {
	return &Service{store: s, indexer: idx, queue: make(chan string, 128), planner: PlannerAgent{}, codebase: CodebaseAgent{Retriever: retrieval.New(retrieval.Config{})}, patch: PatchAgent{}, test: TestAgent{Sandbox: sandbox.New(sandbox.Config{})}, reviewer: ReviewerAgent{}}
}

func NewServiceWithLLM(s store.Store, idx *indexer.Indexer, client llm.Client) *Service {
	return &Service{store: s, indexer: idx, queue: make(chan string, 128), planner: PlannerAgent{LLM: client}, codebase: CodebaseAgent{Retriever: retrieval.New(retrieval.Config{})}, patch: PatchAgent{LLM: client}, test: TestAgent{Sandbox: sandbox.New(sandbox.Config{})}, reviewer: ReviewerAgent{LLM: client}}
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
	id, err := s.store.ID("repo")
	if err != nil {
		return domain.Repository{}, err
	}
	repo := domain.Repository{ID: id, Name: strings.TrimSpace(name), Path: path, CreatedAt: now}
	if repo.Name == "" {
		repo.Name = info.Name()
	}
	if err := s.store.AddRepository(repo); err != nil {
		return domain.Repository{}, err
	}
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
	if err := s.store.SetIndex(repo, files, symbols); err != nil {
		return repo, err
	}
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
	id, err := s.store.ID("task")
	if err != nil {
		return domain.Task{}, err
	}
	task := domain.Task{ID: id, RepositoryID: repoID, Title: strings.TrimSpace(title), Description: strings.TrimSpace(description), Status: domain.TaskCreated, CreatedAt: now, UpdatedAt: now}
	if err := s.store.AddTask(task); err != nil {
		return domain.Task{}, err
	}
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
	runID, err := s.store.ID("run")
	if err != nil {
		s.fail(task, "", err)
		return
	}
	if err := s.store.AddRun(domain.TaskRun{ID: runID, TaskID: task.ID, Status: domain.TaskIndexCheck, StartedAt: time.Now().UTC()}); err != nil {
		s.fail(task, "", err)
		return
	}
	if repo.IndexedAt.IsZero() {
		if err := s.store.UpdateTask(task.ID, domain.TaskIndexCheck, ""); err != nil {
			s.fail(task, runID, err)
			return
		}
		repo, err = s.IndexRepository(repo.ID)
		if err != nil {
			s.fail(task, runID, err)
			return
		}
	}
	contextData := map[string]any{}
	plan, err := s.runAgentStep(ctx, task, repo, runID, domain.TaskPlanning, s.planner, contextData, 0)
	if err != nil {
		s.fail(task, runID, err)
		return
	}
	contextData["planner"], contextData["initial_plan"] = plan.Output, plan.Output
	codebase, err := s.runAgentStep(ctx, task, repo, runID, domain.TaskRetrievingContext, s.codebase, contextData, 0)
	if err != nil {
		s.fail(task, runID, err)
		return
	}
	contextData["codebase"] = codebase.Output

	history := []map[string]any{}
	finalDecision := ReviewHumanRequired
	for attempt := 1; attempt <= maxPatchAttempts; attempt++ {
		patchResult, runErr := s.runAgentStep(ctx, task, repo, runID, domain.TaskGeneratingPatch, s.patch, contextData, attempt)
		if runErr != nil {
			s.fail(task, runID, runErr)
			return
		}
		contextData["patch"] = patchResult.Output
		testResult, runErr := s.runAgentStep(ctx, task, repo, runID, domain.TaskRunningTests, s.test, contextData, attempt)
		if runErr != nil {
			s.fail(task, runID, runErr)
			return
		}
		contextData["test"] = testResult.Output
		report, passed := testResult.Output.(sandbox.Report)
		summary := attemptSummary(attempt, report)
		history = append(history, summary)
		contextData["attempt_history"] = history
		if passed && report.Applied && report.Passed {
			reviewResult, reviewErr := s.runAgentStep(ctx, task, repo, runID, domain.TaskReviewing, s.reviewer, contextData, attempt)
			if reviewErr != nil {
				s.fail(task, runID, reviewErr)
				return
			}
			contextData["reviewer"] = reviewResult.Output
			finalDecision = reviewDecisionFromResult(reviewResult.Output)
			summary["review_decision"] = finalDecision
			contextData["attempt_history"] = history
			if finalDecision == ReviewApprove || finalDecision == ReviewHumanRequired || attempt == maxPatchAttempts {
				break
			}
			contextData["repair_feedback"] = reviewFeedback(reviewResult.Output)
			contextData["repair_instruction"] = "The patch applied and tests passed, but Reviewer requested changes. Regenerate the patch to address every review finding and retain passing tests."
		} else {
			if attempt == maxPatchAttempts {
				reviewResult, reviewErr := s.runAgentStep(ctx, task, repo, runID, domain.TaskReviewing, s.reviewer, contextData, attempt)
				if reviewErr != nil {
					s.fail(task, runID, reviewErr)
					return
				}
				contextData["reviewer"] = reviewResult.Output
				finalDecision = reviewDecisionFromResult(reviewResult.Output)
				summary["review_decision"] = finalDecision
				break
			}
			contextData["repair_feedback"] = repairFeedback(report)
			contextData["repair_instruction"] = "Discard the previous diff. Regenerate all hunks from the exact current source in context_pack and address the sandbox error."
		}
		delete(contextData, "patch")
		delete(contextData, "test")
		delete(contextData, "reviewer")
		replan, replanErr := s.runAgentStep(ctx, task, repo, runID, domain.TaskReplanRequired, s.planner, contextData, attempt+1)
		if replanErr != nil {
			s.fail(task, runID, replanErr)
			return
		}
		contextData["planner"] = replan.Output
	}
	finalStatus := domain.TaskCompleted
	if finalDecision != ReviewApprove {
		finalStatus = domain.TaskHumanReview
	}
	if err := s.store.UpdateTask(task.ID, finalStatus, ""); err != nil {
		s.fail(task, runID, err)
		return
	}
	if err := s.store.FinishRun(task.ID, runID, finalStatus); err != nil {
		s.fail(task, runID, err)
		return
	}
	memoryID, err := s.store.ID("memory")
	if err != nil {
		s.fail(task, runID, err)
		return
	}
	if err := s.store.AddMemory(domain.MemoryEntry{ID: memoryID, RepositoryID: repo.ID, TaskID: task.ID, Kind: "execution_summary", Content: fmt.Sprintf("%s: execution ended with review decision %s", task.Title, finalDecision), CreatedAt: time.Now().UTC()}); err != nil {
		s.fail(task, runID, err)
	}
}

func (s *Service) runAgentStep(ctx context.Context, task domain.Task, repo domain.Repository, runID string, status domain.TaskStatus, agent Agent, contextData map[string]any, attempt int) (AgentResult, error) {
	if err := s.store.UpdateTask(task.ID, status, ""); err != nil {
		return AgentResult{}, err
	}
	files, err := s.store.Files(repo.ID)
	if err != nil {
		return AgentResult{}, err
	}
	symbols, err := s.store.Symbols(repo.ID)
	if err != nil {
		return AgentResult{}, err
	}
	started := time.Now().UTC()
	req := AgentRequest{Task: task, Repository: repo, Files: files, Symbols: symbols, Context: cloneContext(contextData), Attempt: attempt}
	result, runErr := agent.Run(ctx, req)
	ended := time.Now().UTC()
	stepID, err := s.store.ID("step")
	if err != nil {
		return AgentResult{}, err
	}
	step := domain.TaskStep{ID: stepID, TaskID: task.ID, RunID: runID, AgentName: agent.Name(), StepType: string(status), Status: "COMPLETED", Input: map[string]any{"task": task.Description, "attempt": attempt}, Output: result.Output, StartedAt: started, EndedAt: ended, LatencyMS: ended.Sub(started).Milliseconds()}
	if runErr != nil {
		step.Status, step.Error = "FAILED", runErr.Error()
	}
	if err := s.store.AddStep(step); err != nil {
		return AgentResult{}, err
	}
	if runErr == nil && result.ArtifactType != "" {
		name := result.ArtifactName
		if attempt > 0 {
			name = fmt.Sprintf("attempt-%d-%s", attempt, name)
		}
		artifactID, err := s.store.ID("artifact")
		if err != nil {
			return AgentResult{}, err
		}
		if err := s.store.AddArtifact(domain.Artifact{ID: artifactID, TaskID: task.ID, RunID: runID, Type: result.ArtifactType, Name: name, Content: result.ArtifactContent, CreatedAt: ended}); err != nil {
			return AgentResult{}, err
		}
	}
	return result, runErr
}

func attemptSummary(attempt int, report sandbox.Report) map[string]any {
	return map[string]any{"attempt": attempt, "status": report.Status, "applied": report.Applied, "passed": report.Passed, "changed_files": report.ChangedFiles, "error": report.Error, "output": truncateFeedback(report.Output)}
}

func repairFeedback(report sandbox.Report) map[string]any {
	return map[string]any{"status": report.Status, "applied": report.Applied, "passed": report.Passed, "changed_files": report.ChangedFiles, "error": report.Error, "output": truncateFeedback(report.Output)}
}

func reviewFeedback(output any) map[string]any {
	result, _ := output.(map[string]any)
	review, _ := result["review"].(string)
	decision, _ := result["decision"].(string)
	return map[string]any{"source": "reviewer", "decision": decision, "review": truncateFeedback(review)}
}

func reviewDecisionFromResult(output any) string {
	result, ok := output.(map[string]any)
	if !ok {
		return ReviewHumanRequired
	}
	decision, _ := result["decision"].(string)
	switch decision {
	case ReviewApprove, ReviewRequestChanges, ReviewHumanRequired:
		return decision
	default:
		return ReviewHumanRequired
	}
}

func truncateFeedback(value string) string {
	if len(value) <= maxRepairFeedbackBytes {
		return value
	}
	return value[:maxRepairFeedbackBytes] + "\n[FEEDBACK TRUNCATED]"
}

func cloneContext(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func (s *Service) fail(task domain.Task, runID string, err error) {
	_ = s.store.UpdateTask(task.ID, domain.TaskFailed, err.Error())
	if runID != "" {
		_ = s.store.FinishRun(task.ID, runID, domain.TaskFailed)
	}
}
