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
	"codecodriver/internal/lease"
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
	leaser      lease.Leaser
	workers     int
	cancelMu    sync.Mutex
	cancelTasks map[string]context.CancelFunc
	queuedMu    sync.Mutex
	queued      map[string]bool
}

const maxPatchAttempts = 3
const maxRepairFeedbackBytes = 8 * 1024
const leaseTTL = 45 * time.Second

func NewService(s store.Store, idx *indexer.Indexer) *Service {
	return newService(s, idx, PlannerAgent{}, PatchAgent{}, ReviewerAgent{})
}

func NewServiceWithLLM(s store.Store, idx *indexer.Indexer, client llm.Client) *Service {
	return newService(s, idx, PlannerAgent{LLM: client}, PatchAgent{LLM: client}, ReviewerAgent{LLM: client})
}

func newService(s store.Store, idx *indexer.Indexer, planner, patch, reviewer Agent) *Service {
	service := &Service{store: s, indexer: idx, queue: make(chan string, 128), planner: planner, codebase: CodebaseAgent{Retriever: retrieval.New(retrieval.Config{})}, patch: patch, test: TestAgent{Sandbox: sandbox.New(sandbox.Config{})}, reviewer: reviewer, workers: workerCount(), cancelTasks: map[string]context.CancelFunc{}, queued: map[string]bool{}, toolGateway: tools.NewGateway()}
	service.configureToolGateway(service.toolGateway)
	if plannerAgent, ok := planner.(PlannerAgent); ok {
		if observer, ok := plannerAgent.LLM.(llm.UsageObserver); ok {
			observer.SetUsageObserver(func(usage llm.Usage) {
				id, err := s.ID("llm")
				if err != nil {
					return
				}
				_ = s.AddLLMUsage(domain.LLMUsage{ID: id, TaskID: usage.TaskID, RunID: usage.RunID, StepID: usage.StepID, AgentName: usage.AgentName, Model: usage.Model, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens, EstimatedCostUSD: usage.EstimatedCostUSD, LatencyMS: usage.LatencyMS, CreatedAt: time.Now().UTC()})
			})
		}
	}
	return service
}

func (s *Service) SetToolGateway(gateway *tools.Gateway) {
	if gateway == nil {
		gateway = tools.NewGateway()
	}
	s.toolGateway = gateway
	s.configureToolGateway(gateway)
}

func (s *Service) SetLeaser(l lease.Leaser) {
	s.leaser = l
}

func (s *Service) SetAgentToolPolicy(agent string, allowed ...string) {
	if s.toolGateway != nil {
		s.toolGateway.SetAgentToolPolicy(agent, allowed...)
	}
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
	if s.leaser != nil {
		s.distributedWorker(ctx)
		return
	}
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

func (s *Service) distributedWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		taskID, claimed, ok := s.claimNextTask(ctx)
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}
		s.executeClaimed(ctx, taskID, claimed)
	}
}

func (s *Service) claimNextTask(ctx context.Context) (string, lease.Lease, bool) {
	tasks, err := s.store.Tasks()
	if err != nil {
		return "", lease.Lease{}, false
	}
	for _, task := range tasks {
		if !claimableStatus(task.Status) {
			continue
		}
		claimed, ok, err := s.leaser.TryClaim(ctx, task.ID, leaseTTL)
		if err != nil || !ok {
			continue
		}
		current, err := s.store.Task(task.ID)
		if err != nil || !claimableStatus(current.Status) {
			_ = s.leaser.Release(ctx, claimed)
			continue
		}
		return task.ID, claimed, true
	}
	return "", lease.Lease{}, false
}

func claimableStatus(status domain.TaskStatus) bool {
	switch status {
	case domain.TaskCreated, domain.TaskIndexCheck, domain.TaskPlanning, domain.TaskRetrievingContext, domain.TaskGeneratingPatch, domain.TaskRunningTests, domain.TaskReviewing, domain.TaskReplanRequired:
		return true
	default:
		return false
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
			if s.leaser == nil {
				s.enqueue(task.ID)
			}
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
			if err := s.store.UpdateTask(task.ID, domain.TaskCreated, "recovered after process restart"); err == nil && s.leaser == nil {
				s.enqueue(task.ID)
			}
		}
	}
}

func (s *Service) enqueue(taskID string) {
	if s.leaser != nil {
		return
	}
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

func (s *Service) RegisterRepository(name, path string, testCommands ...string) (domain.Repository, error) {
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
	testCommand := ""
	if len(testCommands) > 0 {
		testCommand = strings.TrimSpace(testCommands[0])
	}
	repo := domain.Repository{ID: id, Name: strings.TrimSpace(name), Path: path, TestCommand: testCommand, CreatedAt: now}
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
	return s.createTask(repoID, title, description, true)
}

func (s *Service) createTask(repoID, title, description string, enqueue bool) (domain.Task, error) {
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
	if enqueue {
		s.enqueue(task.ID)
	}
	return task, nil
}

func (s *Service) CreateEvaluationTask(caseID, mode string, batchIDs ...string) (domain.EvaluationRun, domain.Task, error) {
	benchmark, err := s.store.BenchmarkCase(caseID)
	if err != nil {
		return domain.EvaluationRun{}, domain.Task{}, err
	}
	if mode == "" {
		mode = "agent"
	}
	task, err := s.createTask(benchmark.RepositoryID, benchmark.Title, benchmark.Description, false)
	if err != nil {
		return domain.EvaluationRun{}, domain.Task{}, err
	}
	now := time.Now().UTC()
	runID, err := s.store.ID("evaluation")
	if err != nil {
		return domain.EvaluationRun{}, domain.Task{}, err
	}
	batchID := ""
	if len(batchIDs) > 0 {
		batchID = batchIDs[0]
	}
	run := domain.EvaluationRun{ID: runID, CaseID: caseID, BatchID: batchID, TaskID: task.ID, Mode: mode, Status: "queued", StartedAt: now, CreatedAt: now}
	if err := s.store.AddEvaluationRun(run); err != nil {
		return domain.EvaluationRun{}, domain.Task{}, err
	}
	s.enqueue(task.ID)
	return run, task, nil
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

func (s *Service) ResolveHumanReview(taskID string, approve bool, reason string) (domain.Task, error) {
	task, err := s.store.Task(taskID)
	if err != nil {
		return task, err
	}
	if task.Status != domain.TaskHumanReview {
		return task, fmt.Errorf("task is not waiting for human review: %s", task.Status)
	}
	reason = strings.TrimSpace(reason)
	status := domain.TaskFailed
	message := "rejected by human reviewer"
	if approve {
		status = domain.TaskCompleted
		message = ""
	}
	if reason != "" {
		message = reason
	}
	if err := s.store.UpdateTask(task.ID, status, message); err != nil {
		return task, err
	}
	task.Status = status
	task.Error = message
	s.finalizeEvaluation(task, status)

	runs, _ := s.store.Runs(task.ID)
	runID := ""
	if len(runs) > 0 {
		runID = runs[len(runs)-1].ID
	}
	if id, idErr := s.store.ID("artifact"); idErr == nil {
		decision := "rejected"
		if approve {
			decision = "approved"
		}
		_ = s.store.AddArtifact(domain.Artifact{
			ID:        id,
			TaskID:    task.ID,
			RunID:     runID,
			Type:      "human_review",
			Name:      "human-decision.json",
			Content:   fmt.Sprintf(`{"decision":%q,"reason":%q}`, decision, reason),
			CreatedAt: time.Now().UTC(),
		})
	}
	return task, nil
}

func (s *Service) execute(ctx context.Context, taskID string) {
	s.executeTask(ctx, taskID, nil)
}

func (s *Service) executeClaimed(ctx context.Context, taskID string, claimed lease.Lease) {
	renewCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.renewLease(renewCtx, claimed)
	}()
	s.executeTask(ctx, taskID, &claimed)
	cancel()
	<-done
	_ = s.leaser.Release(context.Background(), claimed)
}

func (s *Service) renewLease(ctx context.Context, claimed lease.Lease) {
	ticker := time.NewTicker(leaseTTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.leaser.Renew(ctx, claimed, leaseTTL); err != nil {
				return
			}
		}
	}
}

func (s *Service) executeTask(ctx context.Context, taskID string, claimed *lease.Lease) {
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
	token := int64(0)
	if claimed != nil {
		token = claimed.Token
	}
	failRun := func(err error) {
		if claimed != nil {
			s.failForRun(task, runID, token, err)
			return
		}
		s.fail(task, runID, err)
	}
	updateTask := func(status domain.TaskStatus, errorText string) error {
		if claimed != nil {
			return s.store.UpdateTaskForRun(task.ID, runID, token, status, errorText)
		}
		return s.store.UpdateTask(task.ID, status, errorText)
	}
	if err := s.store.AddRun(domain.TaskRun{ID: runID, TaskID: task.ID, Status: domain.TaskIndexCheck, FencingToken: token, StartedAt: time.Now().UTC()}); err != nil {
		s.fail(task, "", err)
		return
	}
	if repo.IndexedAt.IsZero() {
		if err := updateTask(domain.TaskIndexCheck, ""); err != nil {
			failRun(err)
			return
		}
		repo, err = s.IndexRepository(repo.ID)
		if err != nil {
			failRun(err)
			return
		}
	}
	contextData := map[string]any{}
	memoryQuery := task.Title + " " + task.Description
	memories, memoryErr := s.store.SearchMemoryLimit(repo.ID, memoryQuery, 5)
	if memoryErr != nil {
		failRun(memoryErr)
		return
	}
	contextData["memory"] = memories
	if len(memories) > 0 {
		memoryArtifactID, idErr := s.store.ID("artifact")
		if idErr != nil {
			failRun(idErr)
			return
		}
		if addErr := s.store.AddArtifact(domain.Artifact{ID: memoryArtifactID, TaskID: task.ID, RunID: runID, Type: "memory_retrieval", Name: "memory-context.json", Content: marshalMemory(memories), CreatedAt: time.Now().UTC()}); addErr != nil {
			failRun(addErr)
			return
		}
	}
	plan, err := s.runAgentStep(taskCtx, task, repo, runID, token, domain.TaskPlanning, s.planner, contextData, 0)
	if err != nil {
		failRun(err)
		return
	}
	contextData["planner"], contextData["initial_plan"] = plan.Output, plan.Output
	codebase, err := s.runAgentStep(taskCtx, task, repo, runID, token, domain.TaskRetrievingContext, s.codebase, contextData, 0)
	if err != nil {
		failRun(err)
		return
	}
	contextData["codebase"] = codebase.Output

	history := []map[string]any{}
	finalDecision := ReviewHumanRequired
	for attempt := 1; attempt <= maxPatchAttempts; attempt++ {
		patchResult, runErr := s.runAgentStep(taskCtx, task, repo, runID, token, domain.TaskGeneratingPatch, s.patch, contextData, attempt)
		if runErr != nil {
			failRun(runErr)
			return
		}
		contextData["patch"] = patchResult.Output
		testResult, runErr := s.runAgentStep(taskCtx, task, repo, runID, token, domain.TaskRunningTests, s.test, contextData, attempt)
		if runErr != nil {
			failRun(runErr)
			return
		}
		contextData["test"] = testResult.Output
		report, passed := testResult.Output.(sandbox.Report)
		summary := attemptSummary(attempt, report)
		history = append(history, summary)
		contextData["attempt_history"] = history
		if passed && report.Applied && report.Passed {
			reviewResult, reviewErr := s.runAgentStep(taskCtx, task, repo, runID, token, domain.TaskReviewing, s.reviewer, contextData, attempt)
			if reviewErr != nil {
				failRun(reviewErr)
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
				reviewResult, reviewErr := s.runAgentStep(taskCtx, task, repo, runID, token, domain.TaskReviewing, s.reviewer, contextData, attempt)
				if reviewErr != nil {
					failRun(reviewErr)
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
		replan, replanErr := s.runAgentStep(taskCtx, task, repo, runID, token, domain.TaskReplanRequired, s.planner, contextData, attempt+1)
		if replanErr != nil {
			failRun(replanErr)
			return
		}
		contextData["planner"] = replan.Output
	}
	finalStatus := domain.TaskCompleted
	if finalDecision != ReviewApprove {
		finalStatus = domain.TaskHumanReview
	}
	if err := updateTask(finalStatus, ""); err != nil {
		failRun(err)
		return
	}
	var finishErr error
	if claimed != nil {
		finishErr = s.store.FinishRunWithToken(task.ID, runID, finalStatus, token)
	} else {
		finishErr = s.store.FinishRun(task.ID, runID, finalStatus)
	}
	if finishErr != nil {
		failRun(finishErr)
		return
	}
	if err := s.persistExecutionMemories(repo.ID, task, runID, finalDecision, history, contextData); err != nil {
		failRun(err)
	}
	s.finalizeEvaluation(task, finalStatus)
}

func (s *Service) finalizeEvaluation(task domain.Task, status domain.TaskStatus) {
	runs, err := s.store.AllEvaluationRuns()
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, run := range runs {
		if run.TaskID != task.ID || run.Status == "completed" || run.Status == "failed" {
			continue
		}
		run.Status = strings.ToLower(string(status))
		run.Passed = status == domain.TaskCompleted
		run.EndedAt = now
		run.DurationMS = now.Sub(run.StartedAt).Milliseconds()
		if !run.Passed {
			run.Notes = task.Error
		}
		_ = s.store.UpdateEvaluationRun(run)
		if run.BatchID != "" {
			s.refreshEvaluationBatch(run.BatchID)
		}
	}
}

func (s *Service) refreshEvaluationBatch(batchID string) {
	batches, err := s.store.EvaluationBatches()
	if err != nil {
		return
	}
	var batch domain.EvaluationBatch
	found := false
	for _, item := range batches {
		if item.ID == batchID {
			batch, found = item, true
			break
		}
	}
	if !found {
		return
	}
	runs, err := s.store.AllEvaluationRuns()
	if err != nil {
		return
	}
	batch.Completed, batch.Passed = 0, 0
	for _, run := range runs {
		if run.BatchID != batchID {
			continue
		}
		if run.Status == "completed" || run.Status == "failed" || run.Status == "human_review_required" || run.Status == "cancelled" {
			batch.Completed++
			if run.Passed {
				batch.Passed++
			}
		}
	}
	if batch.Completed >= batch.Total && batch.Total > 0 {
		batch.Status = "completed"
		batch.EndedAt = time.Now().UTC()
		var totalDuration int64
		for _, run := range runs {
			if run.BatchID == batchID {
				totalDuration += run.DurationMS
			}
		}
		avgDuration := totalDuration / int64(batch.Total)
		passRate := float64(batch.Passed) / float64(batch.Total)
		if id, idErr := s.store.ID("metric"); idErr == nil {
			_ = s.store.AddEvaluationMetricSnapshot(domain.EvaluationMetricSnapshot{ID: id, BatchID: batch.ID, Mode: batch.Mode, Total: batch.Total, Passed: batch.Passed, PassRate: passRate, AvgDurationMS: avgDuration, CreatedAt: batch.EndedAt})
		}
	}
	_ = s.store.UpdateEvaluationBatch(batch)
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

func (s *Service) runAgentStep(ctx context.Context, task domain.Task, repo domain.Repository, runID string, token int64, status domain.TaskStatus, agent Agent, contextData map[string]any, attempt int) (AgentResult, error) {
	var err error
	if token != 0 {
		err = s.store.UpdateTaskForRun(task.ID, runID, token, status, "")
	} else {
		err = s.store.UpdateTask(task.ID, status, "")
	}
	if err != nil {
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
	toolCtx = tools.WithAgentContext(toolCtx, agent.Name())
	toolCtx = llm.WithExecutionContext(toolCtx, task.ID, runID, stepID, agent.Name())
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
	return map[string]any{"attempt": attempt, "status": report.Status, "error_kind": classifyRepairError(report), "applied": report.Applied, "passed": report.Passed, "changed_files": report.ChangedFiles, "error": report.Error, "output": truncateFeedback(report.Output)}
}

func repairFeedback(report sandbox.Report) map[string]any {
	return map[string]any{"status": report.Status, "error_kind": classifyRepairError(report), "applied": report.Applied, "passed": report.Passed, "changed_files": report.ChangedFiles, "error": report.Error, "output": truncateFeedback(report.Output)}
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

func classifyRepairError(report sandbox.Report) string {
	switch report.Status {
	case "invalid_patch":
		return "invalid_patch"
	case "tests_failed":
		return "tests_failed"
	case "apply_failed":
		joined := strings.ToLower(report.Error + " " + report.Output)
		switch {
		case strings.Contains(joined, "already exists"):
			return "file_already_exists"
		case strings.Contains(joined, "does not apply") || strings.Contains(joined, "patch failed"):
			return "stale_or_invalid_hunk"
		case strings.Contains(joined, "recount"):
			return "malformed_diff"
		default:
			return "apply_failed"
		}
	default:
		return "unknown"
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
	s.failForRun(task, runID, 0, err)
}

func (s *Service) failForRun(task domain.Task, runID string, token int64, err error) {
	if current, getErr := s.store.Task(task.ID); getErr == nil && current.Status == domain.TaskCancelled {
		if runID != "" {
			if token != 0 {
				_ = s.store.FinishRunWithToken(task.ID, runID, domain.TaskCancelled, token)
			} else {
				_ = s.store.FinishRun(task.ID, runID, domain.TaskCancelled)
			}
		}
		return
	}
	if token != 0 {
		updateErr := s.store.UpdateTaskForRun(task.ID, runID, token, domain.TaskFailed, err.Error())
		if updateErr == store.ErrStaleRun {
			return
		}
	} else {
		_ = s.store.UpdateTask(task.ID, domain.TaskFailed, err.Error())
	}
	if runID != "" {
		if token != 0 {
			_ = s.store.FinishRunWithToken(task.ID, runID, domain.TaskFailed, token)
		} else {
			_ = s.store.FinishRun(task.ID, runID, domain.TaskFailed)
		}
	}
	task.Error = err.Error()
	s.finalizeEvaluation(task, domain.TaskFailed)
}
