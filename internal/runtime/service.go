package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/lease"
	"codecodriver/internal/llm"
	agentmemory "codecodriver/internal/memory"
	"codecodriver/internal/retrieval"
	"codecodriver/internal/sandbox"
	"codecodriver/internal/skills"
	"codecodriver/internal/store"
	"codecodriver/internal/tools"
)

type Service struct {
	store           store.Store
	indexer         *indexer.Indexer
	queue           chan string
	planner         Agent
	codebase        Agent
	patch           Agent
	test            Agent
	reviewer        Agent
	toolGateway     *tools.Gateway
	memoryRefiner   *agentmemory.Service
	memoryQueue     chan []domain.MemoryEntry
	memoryPending   map[string]bool
	memoryWorkers   int
	memoryPendingMu sync.Mutex
	leaser          lease.Leaser
	skillRegistry   *skills.Registry
	taskRouter      *skills.Router
	workers         int
	cancelMu        sync.Mutex
	cancelTasks     map[string]context.CancelFunc
	queuedMu        sync.Mutex
	queued          map[string]bool
}

const maxPatchAttempts = 3
const maxRepairFeedbackBytes = 8 * 1024
const leaseTTL = 45 * time.Second
const memoryQueueCapacity = 256
const maxMemoryRefineAttempts = 3

func NewService(s store.Store, idx *indexer.Indexer) *Service {
	return newService(s, idx, PlannerAgent{}, PatchAgent{}, ReviewerAgent{})
}

func NewServiceWithLLM(s store.Store, idx *indexer.Indexer, client llm.Client) *Service {
	service := newService(s, idx, PlannerAgent{LLM: client}, PatchAgent{LLM: client}, ReviewerAgent{LLM: client})
	service.memoryRefiner = agentmemory.New(s, client)
	return service
}

func newService(s store.Store, idx *indexer.Indexer, planner, patch, reviewer Agent) *Service {
	registry := skills.DefaultRegistry()
	service := &Service{store: s, indexer: idx, queue: make(chan string, 128), memoryQueue: make(chan []domain.MemoryEntry, memoryQueueCapacity), memoryPending: map[string]bool{}, memoryWorkers: memoryWorkerCount(), planner: planner, codebase: CodebaseAgent{Retriever: retrieval.New(retrieval.Config{})}, patch: patch, test: TestAgent{Sandbox: sandbox.New(sandbox.Config{})}, reviewer: reviewer, skillRegistry: registry, taskRouter: skills.NewRouter(registry), workers: workerCount(), cancelTasks: map[string]context.CancelFunc{}, queued: map[string]bool{}, toolGateway: tools.NewGateway()}
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

func (s *Service) SetSkillRegistry(registry *skills.Registry) {
	if registry == nil {
		registry = skills.DefaultRegistry()
	}
	s.skillRegistry = registry
	s.taskRouter = skills.NewRouter(registry)
}

func (s *Service) ListSkills() []skills.Skill {
	if s.skillRegistry == nil {
		return []skills.Skill{}
	}
	return s.skillRegistry.List()
}

func (s *Service) RegisterSkill(skill skills.Skill) error {
	if s.skillRegistry == nil {
		s.SetSkillRegistry(nil)
	}
	return s.skillRegistry.Register(skill)
}

func (s *Service) LoadSkillFile(path string) error {
	if s.skillRegistry == nil {
		s.SetSkillRegistry(nil)
	}
	return s.skillRegistry.LoadFile(path)
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
	for i := 0; i < s.memoryWorkers; i++ {
		go s.memoryWorker(ctx)
	}
	s.recoverMemoryRefinement(ctx)
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

func memoryWorkerCount() int {
	n := 1
	if raw := os.Getenv("CODECODRIVER_MEMORY_WORKERS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 8 {
			n = parsed
		}
	}
	return n
}

func (s *Service) memoryWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case entries := <-s.memoryQueue:
			s.runMemoryRefinementWithRetry(ctx, entries)
			s.clearMemoryPending(entries)
		}
	}
}

func (s *Service) runMemoryRefinementWithRetry(ctx context.Context, entries []domain.MemoryEntry) {
	if s.memoryRefiner == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("memory refinement panic recovered: %v", recovered)
		}
	}()
	for attempt := 1; attempt <= maxMemoryRefineAttempts; attempt++ {
		if err := s.memoryRefiner.Process(ctx, entries); err == nil {
			return
		} else {
			log.Printf("memory refinement attempt %d/%d failed for %d entries: %v", attempt, maxMemoryRefineAttempts, len(entries), err)
		}
		if attempt < maxMemoryRefineAttempts {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}
	log.Printf("memory refinement gave up after %d attempts for %d entries", maxMemoryRefineAttempts, len(entries))
}

func (s *Service) enqueueMemoryRefinement(entries []domain.MemoryEntry) {
	if s.memoryRefiner == nil || s.memoryQueue == nil || len(entries) == 0 {
		return
	}
	s.memoryPendingMu.Lock()
	fresh := make([]domain.MemoryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.ID == "" || s.memoryPending[entry.ID] {
			continue
		}
		s.memoryPending[entry.ID] = true
		fresh = append(fresh, entry)
	}
	s.memoryPendingMu.Unlock()
	if len(fresh) == 0 {
		return
	}
	select {
	case s.memoryQueue <- fresh:
	default:
		s.memoryPendingMu.Lock()
		for _, entry := range fresh {
			delete(s.memoryPending, entry.ID)
		}
		s.memoryPendingMu.Unlock()
		log.Printf("memory refinement queue full; %d entries will be recovered by startup scan", len(fresh))
	}
}

func (s *Service) clearMemoryPending(entries []domain.MemoryEntry) {
	s.memoryPendingMu.Lock()
	defer s.memoryPendingMu.Unlock()
	for _, entry := range entries {
		delete(s.memoryPending, entry.ID)
	}
}

func (s *Service) recoverMemoryRefinement(ctx context.Context) {
	if s.memoryRefiner == nil {
		return
	}
	entries, err := s.store.UnrefinedMemories(50)
	if err != nil {
		log.Printf("recover unrefined memories: %v", err)
		return
	}
	if len(entries) == 0 {
		return
	}
	log.Printf("recovering %d unrefined memories", len(entries))
	s.enqueueMemoryRefinement(entries)
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
	return s.createTask(repoID, title, description, "", domain.MemoryModeWith, true)
}

func (s *Service) CreateTaskWithSkill(repoID, title, description, skillName string) (domain.Task, error) {
	return s.createTask(repoID, title, description, skillName, domain.MemoryModeWith, true)
}

func (s *Service) createTask(repoID, title, description, skillName, memoryMode string, enqueue bool) (domain.Task, error) {
	if _, err := s.store.Repository(repoID); err != nil {
		return domain.Task{}, err
	}
	if strings.TrimSpace(description) == "" {
		return domain.Task{}, fmt.Errorf("description is required")
	}
	if skillName = strings.TrimSpace(skillName); skillName != "" {
		if s.skillRegistry == nil {
			s.SetSkillRegistry(nil)
		}
		if _, ok := s.skillRegistry.Get(skillName); !ok {
			return domain.Task{}, fmt.Errorf("unknown skill %q", skillName)
		}
	}
	now := time.Now().UTC()
	id, err := s.store.ID("task")
	if err != nil {
		return domain.Task{}, err
	}
	if memoryMode == "" {
		memoryMode = domain.MemoryModeWith
	}
	task := domain.Task{ID: id, RepositoryID: repoID, Title: strings.TrimSpace(title), Description: strings.TrimSpace(description), SkillName: skillName, Status: domain.TaskCreated, MemoryMode: memoryMode, CreatedAt: now, UpdatedAt: now}
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
	task, err := s.createTask(benchmark.RepositoryID, benchmark.Title, benchmark.Description, "", memoryModeForEvaluation(mode), false)
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

func memoryModeForEvaluation(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case domain.MemoryModeWithout, "baseline":
		return domain.MemoryModeWithout
	default:
		return domain.MemoryModeWith
	}
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
	if !approve && s.hasPlannerSkipProposal(task.ID) {
		task.Status = domain.TaskCreated
		task.Error = ""
		if err := s.store.UpdateTask(task.ID, domain.TaskCreated, ""); err != nil {
			return task, err
		}
		runID := latestRunID(s.store, task.ID)
		s.recordHumanDecision(task.ID, runID, false, reason)
		if id, idErr := s.store.ID("artifact"); idErr == nil {
			_ = s.store.AddArtifact(domain.Artifact{
				ID:        id,
				TaskID:    task.ID,
				RunID:     runID,
				Type:      plannerSkipArtifactType,
				Name:      "planner-skip-continue.json",
				Content:   marshalArtifact(map[string]any{"decision": "continue", "reason": reason}),
				CreatedAt: time.Now().UTC(),
			})
		}
		s.enqueue(task.ID)
		return task, nil
	}
	if err := s.store.UpdateTask(task.ID, status, message); err != nil {
		return task, err
	}
	task.Status = status
	task.Error = message
	s.finalizeEvaluation(task, status, nil)

	runID := latestRunID(s.store, task.ID)
	s.recordHumanDecision(task.ID, runID, approve, reason)
	if !approve {
		s.persistFailureMemory(task, runID, fmt.Errorf("rejected by human reviewer: %s", reason))
	}
	return task, nil
}

func (s *Service) hasPlannerSkipProposal(taskID string) bool {
	artifacts, err := s.store.Artifacts(taskID)
	if err != nil {
		return false
	}
	return hasPlannerArtifactDecision(artifacts, plannerSkipDecision)
}

func (s *Service) recordHumanDecision(taskID, runID string, approve bool, reason string) {
	decision := "rejected"
	if approve {
		decision = "approved"
	}
	if id, idErr := s.store.ID("artifact"); idErr == nil {
		_ = s.store.AddArtifact(domain.Artifact{
			ID:        id,
			TaskID:    taskID,
			RunID:     runID,
			Type:      "human_review",
			Name:      "human-decision.json",
			Content:   fmt.Sprintf(`{"decision":%q,"reason":%q}`, decision, reason),
			CreatedAt: time.Now().UTC(),
		})
	}
}

func latestRunID(store store.Store, taskID string) string {
	runs, _ := store.Runs(taskID)
	if len(runs) > 0 {
		return runs[len(runs)-1].ID
	}
	return ""
}

type ApplyTaskPatchResult struct {
	Files    []string
	Status   string
	Warnings []string
}

func (s *Service) ApplyTaskPatch(taskID string) (ApplyTaskPatchResult, error) {
	task, err := s.store.Task(taskID)
	if err != nil {
		return ApplyTaskPatchResult{}, err
	}
	if task.Status != domain.TaskCompleted {
		return ApplyTaskPatchResult{}, fmt.Errorf("only completed tasks can be applied to the repository")
	}
	repo, err := s.store.Repository(task.RepositoryID)
	if err != nil {
		return ApplyTaskPatchResult{}, err
	}
	artifacts, err := s.store.Artifacts(taskID)
	if err != nil {
		return ApplyTaskPatchResult{}, err
	}
	proposal := ""
	for _, artifact := range artifacts {
		if artifact.Type == "patch_proposal" && strings.Contains(artifact.Name, "proposed-change.diff") {
			proposal = artifact.Content
		}
	}
	if strings.TrimSpace(proposal) == "" {
		return ApplyTaskPatchResult{}, fmt.Errorf("no patch proposal found for task")
	}
	report, applyErr := sandbox.New(sandbox.Config{}).ApplyToRepository(context.Background(), repo.Path, proposal, "CodeCoDriver: apply task "+task.ID)
	if applyErr != nil {
		return ApplyTaskPatchResult{}, applyErr
	}
	if !report.Applied {
		return ApplyTaskPatchResult{}, fmt.Errorf("apply patch failed: %s %s", report.Error, report.Output)
	}
	if id, idErr := s.store.ID("artifact"); idErr == nil {
		_ = s.store.AddArtifact(domain.Artifact{ID: id, TaskID: task.ID, RunID: "", Type: "applied_patch", Name: "applied-patch.json", Content: marshalArtifact(report), CreatedAt: time.Now().UTC()})
	}
	warnings := splitLines(report.Output)
	if _, indexErr := s.IndexRepository(repo.ID); indexErr != nil {
		warnings = append(warnings, "repository re-index failed: "+indexErr.Error())
	}
	if len(report.ChangedFiles) > 0 {
		runID := ""
		if runs, _ := s.store.Runs(task.ID); len(runs) > 0 {
			runID = runs[len(runs)-1].ID
		}
		now := time.Now().UTC()
		summary := fmt.Sprintf("Applied task patch to repository. Files: %s", strings.Join(report.ChangedFiles, ", "))
		if id, idErr := s.store.ID("memory"); idErr == nil {
			memory := domain.MemoryEntry{
				ID:           id,
				RepositoryID: repo.ID,
				TaskID:       task.ID,
				Kind:         "execution_success",
				Title:        task.Title,
				Summary:      summary,
				Content:      summary,
				ChangedFiles: report.ChangedFiles,
				Source:       "applier",
				Score:        3,
				SuccessScore: 1,
				SourceRunID:  runID,
				CreatedAt:    now,
			}
			_ = s.store.AddMemory(memory)
			s.persistMemoryLinks(memory, runID)
		}
	}
	return ApplyTaskPatchResult{
		Files:    report.ChangedFiles,
		Status:   report.Status,
		Warnings: warnings,
	}, nil
}

func (s *Service) RerunTask(taskID string) (domain.Task, error) {
	original, err := s.store.Task(taskID)
	if err != nil {
		return domain.Task{}, err
	}
	switch original.Status {
	case domain.TaskCompleted, domain.TaskFailed, domain.TaskCancelled, domain.TaskHumanReview:
	default:
		return domain.Task{}, fmt.Errorf("task is still active and cannot be rerun")
	}
	memoryMode := original.MemoryMode
	if memoryMode == "" {
		memoryMode = domain.MemoryModeWith
	}
	return s.createTask(original.RepositoryID, original.Title, original.Description, original.SkillName, memoryMode, true)
}

func splitLines(value string) []string {
	out := []string{}
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
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
	contextData["memory"] = []domain.MemoryEntry{}
	contextData["memory_hits"] = 0
	contextData["memory_success_hits"] = 0
	contextData["memory_failure_hits"] = 0
	contextData["memory_resolved_hits"] = 0
	contextData["memory_refined_hits"] = 0
	if task.MemoryMode != domain.MemoryModeWithout {
		memoryQuery := task.Title + " " + task.Description
		memories, memoryErr := s.store.SearchMemoryLimit(repo.ID, memoryQuery, 10)
		if memoryErr != nil {
			failRun(memoryErr)
			return
		}
		selected := selectMemoryForContext(memories, memoryQuery)
		contextData["memory"] = selected
		contextData["memory_candidates"] = memories
		contextData["memory_hits"] = len(selected)
		contextData["memory_success_hits"], contextData["memory_failure_hits"], contextData["memory_resolved_hits"], contextData["memory_refined_hits"] = memorySourceCounts(selected)
		if len(selected) > 0 {
			memoryArtifactID, idErr := s.store.ID("artifact")
			if idErr != nil {
				failRun(idErr)
				return
			}
			if addErr := s.store.AddArtifact(domain.Artifact{ID: memoryArtifactID, TaskID: task.ID, RunID: runID, Type: "memory_retrieval", Name: "memory-context.json", Content: marshalMemory(selected), CreatedAt: time.Now().UTC()}); addErr != nil {
				failRun(addErr)
				return
			}
		}
	}
	route := skills.RouteResult{
		PrimarySkill: "general",
		Workflow:     "standard_agent_loop",
		Skills:       []skills.Skill{{Name: "general", Workflow: "standard_agent_loop"}},
		Reason:       "fallback general workflow",
		Scores:       map[string]float64{"general": 0},
	}
	if s.taskRouter != nil {
		filesForRoute, filesErr := s.store.Files(repo.ID)
		if filesErr != nil {
			failRun(filesErr)
			return
		}
		memoriesForRoute, _ := contextData["memory_candidates"].([]domain.MemoryEntry)
		route, err = s.taskRouter.Route(skills.RouteInput{Task: task, Repository: repo, Files: filesForRoute, Memories: memoriesForRoute})
		if err != nil {
			failRun(err)
			return
		}
	}
	contextData["skills"] = route.Skills
	contextData["skill_selection"] = route
	contextData["selected_skill"] = route.PrimarySkill
	contextData["selected_workflow"] = route.Workflow
	if skillArtifactID, idErr := s.store.ID("artifact"); idErr == nil {
		_ = s.store.AddArtifact(domain.Artifact{ID: skillArtifactID, TaskID: task.ID, RunID: runID, Type: "skill_selection", Name: "skill-selection.json", Content: marshalArtifact(route), CreatedAt: time.Now().UTC()})
	}
	plan, err := s.runAgentStep(taskCtx, task, repo, runID, token, domain.TaskPlanning, s.planner, contextData, 0)
	if err != nil {
		failRun(err)
		return
	}
	if plannerDecisionFromResult(plan.Output) == PlannerSkipDecision {
		skipReason := plannerSkipReason(plan.Output)
		if err := updateTask(domain.TaskHumanReview, "planner suggested skip: "+skipReason); err != nil {
			failRun(err)
			return
		}
		var finishErr error
		if claimed != nil {
			finishErr = s.store.FinishRunWithToken(task.ID, runID, domain.TaskHumanReview, token)
		} else {
			finishErr = s.store.FinishRun(task.ID, runID, domain.TaskHumanReview)
		}
		if finishErr != nil {
			failRun(finishErr)
			return
		}
		s.finalizeEvaluation(task, domain.TaskHumanReview, contextData)
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
	contextData["repair_attempts"] = len(history)
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
	if task.MemoryMode != domain.MemoryModeWithout {
		createdMemories, err := s.persistExecutionMemories(repo, task, runID, finalDecision, history, contextData)
		if err != nil {
			failRun(err)
		} else if s.memoryRefiner != nil {
			s.enqueueMemoryRefinement(createdMemories)
		}
	}
	s.finalizeEvaluation(task, finalStatus, contextData)
}

func (s *Service) finalizeEvaluation(task domain.Task, status domain.TaskStatus, contextData map[string]any) {
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
		if contextData != nil {
			run.MemoryHits = contextInt(contextData, "memory_hits")
			run.RepairAttempts = contextInt(contextData, "repair_attempts")
			run.MemorySuccessHits = contextInt(contextData, "memory_success_hits")
			run.MemoryFailureHits = contextInt(contextData, "memory_failure_hits")
			run.MemoryResolvedHits = contextInt(contextData, "memory_resolved_hits")
			run.MemoryRefinedHits = contextInt(contextData, "memory_refined_hits")
		}
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
	completedRuns, failedRuns := 0, 0
	for _, run := range runs {
		if run.BatchID != batchID {
			continue
		}
		if run.Status == "completed" {
			completedRuns++
		}
		if run.Status == "failed" {
			failedRuns++
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
		passRate := 0.0
		if completedRuns+failedRuns > 0 {
			passRate = float64(batch.Passed) / float64(completedRuns+failedRuns)
		}
		if id, idErr := s.store.ID("metric"); idErr == nil {
			_ = s.store.AddEvaluationMetricSnapshot(domain.EvaluationMetricSnapshot{ID: id, BatchID: batch.ID, Mode: batch.Mode, Total: batch.Total, Passed: batch.Passed, PassRate: passRate, AvgDurationMS: avgDuration, CreatedAt: batch.EndedAt})
		}
	}
	_ = s.store.UpdateEvaluationBatch(batch)
}

func (s *Service) persistExecutionMemories(repo domain.Repository, task domain.Task, runID, decision string, history []map[string]any, contextData map[string]any) ([]domain.MemoryEntry, error) {
	now := time.Now().UTC()
	files := memoryContextFiles(contextData)
	symbols := memoryContextSymbols(contextData)
	created := []domain.MemoryEntry{}
	add := func(kind, source, content string, score float64, metadata map[string]string, base domain.MemoryEntry) (domain.MemoryEntry, error) {
		id, err := s.store.ID("memory")
		if err != nil {
			return domain.MemoryEntry{}, err
		}
		base.ID = id
		base.RepositoryID = repo.ID
		base.TaskID = task.ID
		base.Kind = kind
		base.Content = content
		base.Source = source
		base.Score = score
		base.Metadata = metadata
		base.CreatedAt = now
		if base.Title == "" {
			base.Title = task.Title
		}
		if base.Summary == "" {
			base.Summary = content
		}
		if base.SourceRunID == "" {
			base.SourceRunID = runID
		}
		if len(base.ChangedFiles) == 0 {
			base.ChangedFiles = files
		}
		if len(base.Symbols) == 0 {
			base.Symbols = symbols
		}
		if base.TestCommand == "" {
			base.TestCommand = repo.TestCommand
		}
		if err := s.store.AddMemory(base); err != nil {
			return domain.MemoryEntry{}, err
		}
		s.persistMemoryLinks(base, runID)
		return base, nil
	}
	summary := fmt.Sprintf("%s: execution ended with review decision %s", task.Title, decision)
	if entry, err := add("execution_summary", "runtime", summary, 1, map[string]string{"decision": decision, "run_id": runID}, domain.MemoryEntry{}); err != nil {
		return nil, err
	} else {
		created = append(created, entry)
	}
	if decision == ReviewApprove {
		content := fmt.Sprintf("Successful engineering pattern for %s: sandbox validation and reviewer approval completed. Files: %s", task.Title, strings.Join(files, ","))
		base := domain.MemoryEntry{SuccessScore: 1, VerificationEvidence: verificationEvidence(contextData)}
		if entry, err := add("execution_success", "reviewer", content, 3, map[string]string{"decision": decision, "run_id": runID}, base); err != nil {
			return nil, err
		} else {
			created = append(created, entry)
		}
	}
	for _, item := range history {
		status, _ := item["status"].(string)
		if status == "passed" {
			continue
		}
		payload, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		symptom, _ := item["error"].(string)
		if symptom == "" {
			symptom = status
		}
		base := domain.MemoryEntry{
			Symptom:              symptom,
			RootCause:            memoryRootCause(status),
			VerificationEvidence: string(payload),
		}
		content := fmt.Sprintf("Failed validation pattern for %s: %s", task.Title, symptom)
		if entry, err := add("failure_pattern", "sandbox", content, 2, map[string]string{"attempt": fmt.Sprint(item["attempt"]), "status": status}, base); err != nil {
			return nil, err
		} else {
			created = append(created, entry)
		}
	}
	return created, nil
}

func (s *Service) persistMemoryLinks(memory domain.MemoryEntry, runID string) {
	sourceRun := runID
	if sourceRun == "" {
		sourceRun = memory.SourceRunID
	}
	add := func(targetType, targetID, label string) {
		if targetType == "" || targetID == "" {
			return
		}
		id, err := s.store.ID("memory_link")
		if err != nil {
			log.Printf("create memory link id: %v", err)
			return
		}
		if err := s.store.AddMemoryLink(domain.MemoryLink{
			ID:           id,
			MemoryID:     memory.ID,
			RepositoryID: memory.RepositoryID,
			TargetType:   targetType,
			TargetID:     targetID,
			Label:        label,
			CreatedAt:    time.Now().UTC(),
		}); err != nil {
			log.Printf("persist memory link: %v", err)
		}
	}
	add("task", memory.TaskID, "source_task")
	add("run", sourceRun, "source_run")
	for _, file := range memory.ChangedFiles {
		add("file", file, "changed_file")
	}
	for _, symbol := range memory.Symbols {
		add("symbol", symbol, "related_symbol")
	}
}

func memoryContextFiles(contextData map[string]any) []string {
	if codebase, ok := contextData["codebase"].(map[string]any); ok {
		if values, ok := codebase["files"].([]string); ok {
			return append([]string(nil), values...)
		}
	}
	return nil
}

func memoryContextSymbols(contextData map[string]any) []string {
	if codebase, ok := contextData["codebase"].(map[string]any); ok {
		if values, ok := codebase["symbols"].([]string); ok {
			return append([]string(nil), values...)
		}
	}
	return nil
}

func contextInt(contextData map[string]any, key string) int {
	if contextData == nil {
		return 0
	}
	value, _ := contextData[key].(int)
	return value
}

func selectMemoryForContext(memories []domain.MemoryEntry, query string) []domain.MemoryEntry {
	high := []domain.MemoryEntry{}
	failures := []domain.MemoryEntry{}
	for _, memory := range memories {
		if memory.DuplicateOf != "" {
			continue
		}
		switch memory.Kind {
		case "resolved_pattern", "execution_success", "refined_execution_success":
			high = append(high, memory)
		case "failure_pattern", "refined_failure_pattern":
			if failureMemoryRelevant(memory, query) {
				failures = append(failures, memory)
			}
		}
	}
	sort.SliceStable(high, func(i, j int) bool {
		return memoryPriorityScore(high[i]) > memoryPriorityScore(high[j])
	})
	sort.SliceStable(failures, func(i, j int) bool {
		return memoryPriorityScore(failures[i]) > memoryPriorityScore(failures[j])
	})
	out := append(high, failures...)
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

func memoryPriorityScore(memory domain.MemoryEntry) float64 {
	score := memory.SuccessScore
	if memory.Kind == "resolved_pattern" {
		score += 1
	}
	if strings.HasPrefix(memory.Kind, "refined_") {
		score += 0.5
	}
	return score + memory.Score*0.1
}

func failureMemoryRelevant(memory domain.MemoryEntry, query string) bool {
	memoryText := strings.ToLower(strings.Join([]string{
		memory.Title,
		memory.Symptom,
		memory.RootCause,
		memory.Condition,
		memory.Summary,
	}, " "))
	return runtimeMemoryTextRelevant(query, memoryText)
}

func runtimeMemoryTextRelevant(query, text string) bool {
	queryTokens := runtimeMemoryTokens(query)
	textTokens := runtimeMemoryTokens(text)
	if len(queryTokens) == 0 {
		return strings.Contains(strings.ToLower(text), strings.ToLower(query))
	}
	for _, queryToken := range queryTokens {
		for _, textToken := range textTokens {
			if queryToken == textToken || fuzzyRuntimeMemoryTokenMatch(queryToken, textToken) {
				return true
			}
		}
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(query))
}

func runtimeMemoryTokens(value string) []string {
	lower := strings.ToLower(value)
	seen := map[string]bool{}
	out := []string{}
	add := func(token string) {
		if token == "" || seen[token] {
			return
		}
		seen[token] = true
		out = append(out, token)
	}
	for _, token := range strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if len(token) >= 3 {
			add(token)
		}
	}
	runes := []rune(lower)
	for i := 0; i < len(runes)-1; i++ {
		if unicode.Is(unicode.Han, runes[i]) || unicode.Is(unicode.Han, runes[i+1]) {
			add(string(runes[i : i+2]))
		}
	}
	return out
}

func fuzzyRuntimeMemoryTokenMatch(left, right string) bool {
	if len(left) < 4 || len(right) < 4 {
		return false
	}
	distance := runtimeEditDistance(left, right)
	maxDistance := len(left) / 3
	if len(right)/3 > maxDistance {
		maxDistance = len(right) / 3
	}
	if maxDistance < 1 {
		maxDistance = 1
	}
	return distance <= maxDistance
}

func runtimeEditDistance(left, right string) int {
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	previous := make([]int, len(rightRunes)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, leftRune := range leftRunes {
		current := make([]int, len(rightRunes)+1)
		current[0] = i + 1
		for j, rightRune := range rightRunes {
			cost := 0
			if leftRune != rightRune {
				cost = 1
			}
			current[j+1] = runtimeMin(previous[j+1]+1, current[j]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(rightRunes)]
}

func runtimeMin(left, middle, right int) int {
	if middle < left {
		left = middle
	}
	if right < left {
		return right
	}
	return left
}

func memorySourceCounts(memories []domain.MemoryEntry) (success, failure, resolved, refined int) {
	for _, memory := range memories {
		switch memory.Kind {
		case "execution_success", "refined_execution_success":
			success++
		case "failure_pattern", "refined_failure_pattern":
			failure++
		case "resolved_pattern":
			resolved++
		}
		if strings.HasPrefix(memory.Kind, "refined_") {
			refined++
		}
	}
	return success, failure, resolved, refined
}

func memoryRootCause(status string) string {
	switch status {
	case "apply_failed":
		return "patch apply failure"
	case "test_failed":
		return "tests did not pass"
	case "invalid_patch":
		return "patch format invalid"
	default:
		return "sandbox validation failure"
	}
}

func verificationEvidence(contextData map[string]any) string {
	evidence := map[string]any{}
	if patch, ok := contextData["patch"].(map[string]any); ok {
		if proposal, ok := patch["proposal"].(string); ok && strings.TrimSpace(proposal) != "" {
			evidence["patch"] = proposal
		}
	}
	if report, ok := contextData["test"].(sandbox.Report); ok {
		evidence["test_status"] = report.Status
		evidence["applied"] = report.Applied
		evidence["passed"] = report.Passed
		if strings.TrimSpace(report.Output) != "" {
			evidence["test_output"] = report.Output
		}
	}
	if len(evidence) == 0 {
		return "sandbox validation and reviewer approval completed"
	}
	content, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return "sandbox validation and reviewer approval completed"
	}
	return truncateFeedback(string(content))
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
	var artifacts []domain.Artifact
	if agent.Name() == "planner" {
		artifacts, err = s.store.Artifacts(task.ID)
		if err != nil {
			return AgentResult{}, err
		}
	}
	req := AgentRequest{Task: task, Repository: repo, Files: files, Symbols: symbols, Artifacts: artifacts, Context: cloneContext(contextData), Attempt: attempt, Tools: s.toolGateway}
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
	s.finalizeEvaluation(task, domain.TaskFailed, nil)
	s.persistFailureMemory(task, runID, err)
}

func (s *Service) persistFailureMemory(task domain.Task, runID string, err error) {
	if runID == "" || task.MemoryMode == domain.MemoryModeWithout {
		return
	}
	repo, getErr := s.store.Repository(task.RepositoryID)
	if getErr != nil {
		return
	}
	id, idErr := s.store.ID("memory")
	if idErr != nil {
		return
	}
	now := time.Now().UTC()
	content := fmt.Sprintf("Agent loop failure for %s: %s", task.Title, err)
	memory := domain.MemoryEntry{
		ID:                   id,
		RepositoryID:         repo.ID,
		TaskID:               task.ID,
		Kind:                 "failure_pattern",
		Title:                task.Title,
		Summary:              content,
		Content:              content,
		Symptom:              err.Error(),
		RootCause:            "agent loop failure",
		TestCommand:          repo.TestCommand,
		VerificationEvidence: err.Error(),
		SourceRunID:          runID,
		Source:               "runtime",
		Score:                2,
		Metadata:             map[string]string{"stage": "agent_loop"},
		CreatedAt:            now,
	}
	if err := s.store.AddMemory(memory); err != nil {
		log.Printf("persist failure memory: %v", err)
		return
	}
	s.persistMemoryLinks(memory, runID)
	if s.memoryRefiner != nil {
		s.enqueueMemoryRefinement([]domain.MemoryEntry{memory})
	}
}
