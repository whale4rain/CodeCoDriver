package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/llm"
	"codecodriver/internal/retrieval"
	"codecodriver/internal/sandbox"
	"codecodriver/internal/store"
	"codecodriver/internal/tools"
)

type Service struct {
	store       store.Store
	indexer     *indexer.Indexer
	queue       chan string
	planner     Agent
	codebase    Agent
	patch       Agent
	test        Agent
	reviewer    Agent
	toolGateway *tools.Gateway
	workers     int
	cancelMu    sync.Mutex
	cancelTasks map[string]context.CancelFunc
	queuedMu    sync.Mutex
	queued      map[string]bool
}

const maxPatchAttempts = 3
const maxRepairFeedbackBytes = 8 * 1024

func NewService(s store.Store, idx *indexer.Indexer) *Service {
	return newService(s, idx, PlannerAgent{}, PatchAgent{}, ReviewerAgent{})
}

func NewServiceWithLLM(s store.Store, idx *indexer.Indexer, client llm.Client) *Service {
	return newService(s, idx, PlannerAgent{LLM: client}, PatchAgent{LLM: client}, ReviewerAgent{LLM: client})
}

func newService(s store.Store, idx *indexer.Indexer, planner, patch, reviewer Agent) *Service {
	service := &Service{store: s, indexer: idx, queue: make(chan string, 128), planner: planner, codebase: CodebaseAgent{Retriever: retrieval.New(retrieval.Config{})}, patch: patch, test: TestAgent{Sandbox: sandbox.New(sandbox.Config{})}, reviewer: reviewer, workers: workerCount(), cancelTasks: map[string]context.CancelFunc{}, queued: map[string]bool{}, toolGateway: tools.NewGateway()}
	service.configureToolGateway(service.toolGateway)
	return service
}

func (s *Service) SetToolGateway(gateway *tools.Gateway) {
	if gateway == nil {
		gateway = tools.NewGateway()
	}
	s.toolGateway = gateway
	s.configureToolGateway(gateway)
}

func (s *Service) configureToolGateway(gateway *tools.Gateway) {
	gateway.Configure(tools.Policy{Timeout: 30 * time.Second}, func(record tools.AuditRecord) {
		id, err := s.store.ID("tool")
		if err != nil {
			return
		}
		status, message := "COMPLETED", ""
		if record.Error != nil {
			status, message = "FAILED", record.Error.Error()
		}
		_ = s.store.AddToolCall(domain.ToolCall{ID: id, TaskID: record.TaskID, RunID: record.RunID, StepID: record.StepID, ToolName: record.Name, ProviderType: "gateway", RequestPayload: record.Request, ResponsePayload: record.Result, Status: status, Error: message, StartedAt: record.StartedAt, EndedAt: record.EndedAt, LatencyMS: record.EndedAt.Sub(record.StartedAt).Milliseconds()})
	})
}

func (s *Service) Start(ctx context.Context) {
	s.ensureRuntimeState()
	for i := 0; i < s.workers; i++ {
		go s.worker(ctx)
	}
	s.recoverTasks()
}

func (s *Service) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case taskID := <-s.queue:
			s.markDequeued(taskID)
			s.execute(ctx, taskID)
		}
	}
}

func (s *Service) recoverTasks() {
	tasks, err := s.store.Tasks()
	if err != nil {
		return
	}
	for _, task := range tasks {
		switch task.Status {
		case domain.TaskCreated:
			s.enqueue(task.ID)
		case domain.TaskReplanRequired, domain.TaskIndexCheck, domain.TaskPlanning, domain.TaskRetrievingContext, domain.TaskGeneratingPatch, domain.TaskRunningTests, domain.TaskReviewing:
			runs, runErr := s.store.Runs(task.ID)
			if runErr != nil {
				continue
			}
			for _, run := range runs {
				if run.EndedAt.IsZero() {
					_ = s.store.FinishRun(task.ID, run.ID, domain.TaskFailed)
				}
			}
			if err := s.store.UpdateTask(task.ID, domain.TaskCreated, "recovered after process restart"); err == nil {
				s.enqueue(task.ID)
			}
		}
	}
}

func (s *Service) enqueue(taskID string) {
	s.ensureRuntimeState()
	s.queuedMu.Lock()
	if s.queued[taskID] {
		s.queuedMu.Unlock()
		return
	}
	s.queued[taskID] = true
	s.queuedMu.Unlock()
	select {
	case s.queue <- taskID:
	case <-time.After(5 * time.Second):
		s.markDequeued(taskID)
	}
}
func (s *Service) markDequeued(taskID string) {
	s.queuedMu.Lock()
	delete(s.queued, taskID)
	s.queuedMu.Unlock()
}
func workerCount() int {
	n := 1
	if raw := os.Getenv("CODECODRIVER_WORKERS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 16 {
			n = parsed
		}
	}
	return n
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
	s.enqueue(task.ID)
	return task, nil
}

func (s *Service) CancelTask(taskID string) error {
	s.ensureRuntimeState()
	task, err := s.store.Task(taskID)
	if err != nil {
		return err
	}
	switch task.Status {
	case domain.TaskCompleted, domain.TaskFailed, domain.TaskCancelled, domain.TaskHumanReview:
		return fmt.Errorf("task is already terminal: %s", task.Status)
	}
	if err := s.store.UpdateTask(taskID, domain.TaskCancelled, "cancelled by user"); err != nil {
		return err
	}
	s.cancelMu.Lock()
	if cancel := s.cancelTasks[taskID]; cancel != nil {
		cancel()
	}
	s.cancelMu.Unlock()
	return nil
}

func (s *Service) execute(ctx context.Context, taskID string) {
	s.ensureRuntimeState()
	task, err := s.store.Task(taskID)
	if err != nil {
		return
	}
	if task.Status == domain.TaskCancelled {
		return
	}
	taskCtx, cancel := context.WithCancel(ctx)
	s.cancelMu.Lock()
	s.cancelTasks[taskID] = cancel
	s.cancelMu.Unlock()
	defer func() {
		cancel()
		s.cancelMu.Lock()
		delete(s.cancelTasks, taskID)
		s.cancelMu.Unlock()
	}()
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
	memoryQuery := task.Title + " " + task.Description
	memories, memoryErr := s.store.SearchMemoryLimit(repo.ID, memoryQuery, 5)
	if memoryErr != nil {
		s.fail(task, runID, memoryErr)
		return
	}
	contextData["memory"] = memories
	if len(memories) > 0 {
		memoryArtifactID, idErr := s.store.ID("artifact")
		if idErr != nil {
			s.fail(task, runID, idErr)
			return
		}
		if addErr := s.store.AddArtifact(domain.Artifact{ID: memoryArtifactID, TaskID: task.ID, RunID: runID, Type: "memory_retrieval", Name: "memory-context.json", Content: marshalMemory(memories), CreatedAt: time.Now().UTC()}); addErr != nil {
			s.fail(task, runID, addErr)
			return
		}
	}
	plan, err := s.runAgentStep(taskCtx, task, repo, runID, domain.TaskPlanning, s.planner, contextData, 0)
	if err != nil {
		s.fail(task, runID, err)
		return
	}
	contextData["planner"], contextData["initial_plan"] = plan.Output, plan.Output
	codebase, err := s.runAgentStep(taskCtx, task, repo, runID, domain.TaskRetrievingContext, s.codebase, contextData, 0)
	if err != nil {
		s.fail(task, runID, err)
		return
	}
	contextData["codebase"] = codebase.Output

	history := []map[string]any{}
	finalDecision := ReviewHumanRequired
	for attempt := 1; attempt <= maxPatchAttempts; attempt++ {
		patchResult, runErr := s.runAgentStep(taskCtx, task, repo, runID, domain.TaskGeneratingPatch, s.patch, contextData, attempt)
		if runErr != nil {
			s.fail(task, runID, runErr)
			return
		}
		contextData["patch"] = patchResult.Output
		testResult, runErr := s.runAgentStep(taskCtx, task, repo, runID, domain.TaskRunningTests, s.test, contextData, attempt)
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
			reviewResult, reviewErr := s.runAgentStep(taskCtx, task, repo, runID, domain.TaskReviewing, s.reviewer, contextData, attempt)
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
				reviewResult, reviewErr := s.runAgentStep(taskCtx, task, repo, runID, domain.TaskReviewing, s.reviewer, contextData, attempt)
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
		replan, replanErr := s.runAgentStep(taskCtx, task, repo, runID, domain.TaskReplanRequired, s.planner, contextData, attempt+1)
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
	if err := s.persistExecutionMemories(repo.ID, task, runID, finalDecision, history, contextData); err != nil {
		s.fail(task, runID, err)
	}
}

func (s *Service) persistExecutionMemories(repositoryID string, task domain.Task, runID, decision string, history []map[string]any, contextData map[string]any) error {
	now := time.Now().UTC()
	add := func(kind, source, content string, score float64, metadata map[string]string) error {
		id, err := s.store.ID("memory")
		if err != nil {
			return err
		}
		return s.store.AddMemory(domain.MemoryEntry{ID: id, RepositoryID: repositoryID, TaskID: task.ID, Kind: kind, Content: content, Source: source, Score: score, Metadata: metadata, CreatedAt: now})
	}
	if err := add("execution_summary", "runtime", fmt.Sprintf("%s: execution ended with review decision %s", task.Title, decision), 1, map[string]string{"decision": decision, "run_id": runID}); err != nil {
		return err
	}
	if decision == ReviewApprove {
		files := ""
		if codebase, ok := contextData["codebase"].(map[string]any); ok {
			if values, ok := codebase["files"].([]string); ok {
				files = strings.Join(values, ",")
			}
		}
		content := fmt.Sprintf("Successful engineering pattern for %s: sandbox validation and reviewer approval completed. Files: %s", task.Title, files)
		if err := add("execution_success", "reviewer", content, 3, map[string]string{"decision": decision, "run_id": runID}); err != nil {
			return err
		}
	}
	for _, item := range history {
		status, _ := item["status"].(string)
		if status == "passed" {
			continue
		}
		payload, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if err := add("failure_pattern", "sandbox", fmt.Sprintf("Failed validation pattern for %s: %s", task.Title, payload), 2, map[string]string{"attempt": fmt.Sprint(item["attempt"]), "status": status}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ensureRuntimeState() {
	s.cancelMu.Lock()
	if s.cancelTasks == nil {
		s.cancelTasks = map[string]context.CancelFunc{}
	}
	s.cancelMu.Unlock()
	s.queuedMu.Lock()
	if s.queued == nil {
		s.queued = map[string]bool{}
	}
	s.queuedMu.Unlock()
	if s.workers <= 0 {
		s.workers = 1
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
	stepID, err := s.store.ID("step")
	if err != nil {
		return AgentResult{}, err
	}
	toolCtx := tools.WithExecutionContext(ctx, task.ID, runID, stepID)
	req := AgentRequest{Task: task, Repository: repo, Files: files, Symbols: symbols, Context: cloneContext(contextData), Attempt: attempt, Tools: s.toolGateway}
	result, runErr := agent.Run(toolCtx, req)
	ended := time.Now().UTC()
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

func marshalMemory(memories []domain.MemoryEntry) string {
	content, err := json.MarshalIndent(memories, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(content)
}

func cloneContext(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func (s *Service) fail(task domain.Task, runID string, err error) {
	if current, getErr := s.store.Task(task.ID); getErr == nil && current.Status == domain.TaskCancelled {
		if runID != "" {
			_ = s.store.FinishRun(task.ID, runID, domain.TaskCancelled)
		}
		return
	}
	_ = s.store.UpdateTask(task.ID, domain.TaskFailed, err.Error())
	if runID != "" {
		_ = s.store.FinishRun(task.ID, runID, domain.TaskFailed)
	}
}
