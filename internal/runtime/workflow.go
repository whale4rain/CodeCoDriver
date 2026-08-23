package runtime

import (
	"context"
	"fmt"

	"codecodriver/internal/domain"
	"codecodriver/internal/lease"
	"codecodriver/internal/sandbox"
	"codecodriver/internal/tools"
)

const (
	WorkflowStandardAgentLoop    = "standard_agent_loop"
	WorkflowDocumentationLoop    = "documentation_agent_loop"
	WorkflowExplanationAgentLoop = "explanation_agent_loop"
	WorkflowDynamicAgentLoop     = "dynamic_agent_loop"

	maxWorkflowSteps = 64
)

type WorkflowNodeKind string

const (
	WorkflowNodeAgent     WorkflowNodeKind = "agent"
	WorkflowNodeDecision  WorkflowNodeKind = "decision"
	WorkflowNodePatchLoop WorkflowNodeKind = "patch_loop"
	WorkflowNodeFinish    WorkflowNodeKind = "finish"
)

type WorkflowDecisionConfig struct {
	Prompt      string   `json:"prompt,omitempty"`
	Options     []string `json:"options,omitempty"`
	DefaultNext string   `json:"default_next,omitempty"`
}

type WorkflowNode struct {
	ID          string                  `json:"id"`
	Kind        WorkflowNodeKind        `json:"kind"`
	AgentName   string                  `json:"agent_name,omitempty"`
	Status      domain.TaskStatus       `json:"status,omitempty"`
	Next        string                  `json:"next,omitempty"`
	NextByKey   map[string]string       `json:"next_by_key,omitempty"`
	OnFailure   string                  `json:"on_failure,omitempty"`
	MaxAttempts int                     `json:"max_attempts,omitempty"`
	Decision    *WorkflowDecisionConfig `json:"decision,omitempty"`
}

type WorkflowSpec struct {
	Name             string         `json:"name"`
	Initial          string         `json:"initial"`
	Nodes            []WorkflowNode `json:"nodes"`
	MaxPatchAttempts int            `json:"max_patch_attempts,omitempty"`
	Terminal         string         `json:"terminal,omitempty"`
}

type WorkflowDecision struct {
	Decision string `json:"decision"`
	Next     string `json:"next_step"`
	Target   string `json:"target,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func (s WorkflowSpec) node(id string) (WorkflowNode, bool) {
	for _, node := range s.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return WorkflowNode{}, false
}

func (s WorkflowSpec) hasNode(id string) bool {
	_, ok := s.node(id)
	return ok
}

func standardWorkflowSpec() WorkflowSpec {
	return WorkflowSpec{
		Name:             WorkflowStandardAgentLoop,
		Initial:          "codebase",
		MaxPatchAttempts: maxPatchAttempts,
		Terminal:         "finish",
		Nodes: []WorkflowNode{
			{ID: "codebase", Kind: WorkflowNodeAgent, AgentName: "codebase", Status: domain.TaskRetrievingContext, Next: "patch_loop"},
			{ID: "patch_loop", Kind: WorkflowNodePatchLoop, Next: "finish"},
			{ID: "finish", Kind: WorkflowNodeFinish},
		},
	}
}

func explanationWorkflowSpec() WorkflowSpec {
	return WorkflowSpec{
		Name:     WorkflowExplanationAgentLoop,
		Initial:  "codebase",
		Terminal: "finish",
		Nodes: []WorkflowNode{
			{ID: "codebase", Kind: WorkflowNodeAgent, AgentName: "codebase", Status: domain.TaskRetrievingContext, Next: "explainer"},
			{ID: "explainer", Kind: WorkflowNodeAgent, AgentName: "explainer", Status: domain.TaskExplaining, Next: "finish"},
			{ID: "finish", Kind: WorkflowNodeFinish},
		},
	}
}

func dynamicWorkflowSpec() WorkflowSpec {
	return WorkflowSpec{
		Name:             WorkflowDynamicAgentLoop,
		Initial:          "orchestrator",
		MaxPatchAttempts: maxPatchAttempts,
		Terminal:         "finish",
		Nodes: []WorkflowNode{
			{
				ID:        "orchestrator",
				Kind:      WorkflowNodeDecision,
				AgentName: "orchestrator",
				Status:    domain.TaskPlanning,
				Next:      "codebase",
				Decision: &WorkflowDecisionConfig{
					Prompt:  "Choose the next workflow step for the task.",
					Options: []string{"code_change", "explain", "request_human"},
				},
			},
			{
				ID:        "codebase",
				Kind:      WorkflowNodeAgent,
				AgentName: "codebase",
				Status:    domain.TaskRetrievingContext,
				Next:      "patch_loop",
				NextByKey: map[string]string{"explainer": "explainer"},
			},
			{ID: "explainer", Kind: WorkflowNodeAgent, AgentName: "explainer", Status: domain.TaskExplaining, Next: "finish"},
			{ID: "patch_loop", Kind: WorkflowNodePatchLoop, Next: "finish"},
			{ID: "finish", Kind: WorkflowNodeFinish},
		},
	}
}

func (s *Service) workflowSpecFor(name string) WorkflowSpec {
	switch name {
	case WorkflowExplanationAgentLoop:
		return explanationWorkflowSpec()
	case WorkflowDynamicAgentLoop:
		return dynamicWorkflowSpec()
	case WorkflowStandardAgentLoop, WorkflowDocumentationLoop:
		return standardWorkflowSpec()
	default:
		return standardWorkflowSpec()
	}
}

func (s *Service) agentForName(name string) Agent {
	switch name {
	case "planner":
		return s.planner
	case "codebase":
		return s.codebase
	case "explainer":
		return s.explainer
	case "patch":
		return s.patch
	case "test":
		return s.test
	case "reviewer":
		return s.reviewer
	case "orchestrator":
		return s.orchestrator
	default:
		return nil
	}
}

func (s *Service) executeWorkflow(ctx context.Context, task domain.Task, repo domain.Repository, runID string, token int64, workflowName string, contextData map[string]any, claimed *lease.Lease, failRun func(error), updateTask func(domain.TaskStatus, string) error) {
	workflowCtx := ctx
	var workspace sandbox.Workspace
	if s.workspaceFactory != nil {
		created, createErr := s.workspaceFactory(ctx, repo.Path)
		if createErr != nil {
			failRun(fmt.Errorf("create task workspace: %w", createErr))
			return
		}
		workspace = created
		workflowCtx = tools.WithWorkspaceContext(ctx, workspace)
		defer func() {
			if workspace != nil {
				_ = workspace.Close(context.Background())
			}
		}()
	}
	spec := s.workflowSpecFor(workflowName)
	current := spec.Initial
	for step := 0; step < maxWorkflowSteps; step++ {
		node, ok := spec.node(current)
		if !ok {
			failRun(fmt.Errorf("workflow %q references unknown node %q", spec.Name, current))
			return
		}
		switch node.Kind {
		case WorkflowNodeAgent:
			agent := s.agentForName(node.AgentName)
			if agent == nil {
				failRun(fmt.Errorf("workflow %q requires unconfigured agent %q", spec.Name, node.AgentName))
				return
			}
			result, runErr := s.runAgentStep(workflowCtx, task, repo, runID, token, node.Status, agent, contextData, 0)
			if runErr != nil {
				failRun(runErr)
				return
			}
			contextData[node.AgentName] = result.Output
			current = node.Next
			if target, ok := contextData["workflow_target"].(string); ok {
				if next, ok := node.NextByKey[target]; ok {
					current = next
				}
			}
			if current == "" {
				failRun(fmt.Errorf("workflow %q node %q has no next step", spec.Name, node.ID))
				return
			}
		case WorkflowNodeDecision:
			decision, decisionErr := s.runWorkflowDecision(workflowCtx, task, repo, runID, token, node, contextData)
			if decisionErr != nil {
				failRun(decisionErr)
				return
			}
			contextData["workflow_decision"] = decision
			contextData["workflow_target"] = decision.Target
			current = decision.Next
			if current == "" {
				current = node.Next
			}
			if current == "" || !spec.hasNode(current) {
				failRun(fmt.Errorf("workflow %q received invalid decision %+v", spec.Name, decision))
				return
			}
			if current == "finish" {
				s.finishWorkflow(task, repo, runID, token, domain.TaskHumanReview, ReviewHumanRequired, contextData, claimed, updateTask, failRun)
				return
			}
		case WorkflowNodePatchLoop:
			finalDecision, loopErr := s.runPatchLoop(workflowCtx, task, repo, runID, token, contextData, spec.MaxPatchAttempts)
			if loopErr != nil {
				failRun(loopErr)
				return
			}
			finalStatus := domain.TaskCompleted
			if finalDecision != ReviewApprove {
				finalStatus = domain.TaskHumanReview
			}
			s.finishWorkflow(task, repo, runID, token, finalStatus, finalDecision, contextData, claimed, updateTask, failRun)
			return
		case WorkflowNodeFinish:
			s.finishWorkflow(task, repo, runID, token, domain.TaskCompleted, ReviewApprove, contextData, claimed, updateTask, failRun)
			return
		default:
			failRun(fmt.Errorf("workflow %q has unknown node kind %q", spec.Name, node.Kind))
			return
		}
	}
	failRun(fmt.Errorf("workflow %q exceeded %d steps", spec.Name, maxWorkflowSteps))
}

func (s *Service) runWorkflowDecision(ctx context.Context, task domain.Task, repo domain.Repository, runID string, token int64, node WorkflowNode, contextData map[string]any) (WorkflowDecision, error) {
	agent := s.agentForName(node.AgentName)
	if agent == nil {
		if node.Next == "" {
			return WorkflowDecision{}, fmt.Errorf("workflow decision agent %q is not configured", node.AgentName)
		}
		return WorkflowDecision{Decision: "fallback", Next: node.Next, Target: "patch_loop", Reason: "orchestrator agent is not configured"}, nil
	}
	result, err := s.runAgentStep(ctx, task, repo, runID, token, node.Status, agent, contextData, 0)
	if err != nil {
		return WorkflowDecision{}, err
	}
	contextData["orchestrator"] = result.Output
	decision := parseWorkflowDecision(result.Output)
	if decision.Next == "" {
		decision.Next = node.Next
	}
	if decision.Target == "" {
		decision.Target = "patch_loop"
	}
	return decision, nil
}

func (s *Service) runPatchLoop(ctx context.Context, task domain.Task, repo domain.Repository, runID string, token int64, contextData map[string]any, maxAttempts int) (string, error) {
	if maxAttempts <= 0 {
		maxAttempts = maxPatchAttempts
	}
	history := []map[string]any{}
	finalDecision := ReviewHumanRequired
	loopCtx := ctx
	var workspace sandbox.Workspace
	workspace = tools.WorkspaceFromContext(ctx)
	createdHere := false
	if workspace == nil && s.workspaceFactory != nil {
		created, createErr := s.workspaceFactory(ctx, repo.Path)
		if createErr != nil {
			return ReviewHumanRequired, fmt.Errorf("create task workspace: %w", createErr)
		}
		workspace = created
		loopCtx = tools.WithWorkspaceContext(ctx, workspace)
		createdHere = true
		defer func() {
			if createdHere && workspace != nil {
				_ = workspace.Close(context.Background())
			}
		}()
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 && workspace != nil {
			if resetErr := workspace.Reset(loopCtx); resetErr != nil {
				return ReviewHumanRequired, fmt.Errorf("reset task workspace: %w", resetErr)
			}
		}
		patchResult, runErr := s.runAgentStep(loopCtx, task, repo, runID, token, domain.TaskGeneratingPatch, s.patch, contextData, attempt)
		if runErr != nil {
			return ReviewHumanRequired, runErr
		}
		contextData["patch"] = patchResult.Output
		testResult, runErr := s.runAgentStep(loopCtx, task, repo, runID, token, domain.TaskRunningTests, s.test, contextData, attempt)
		if runErr != nil {
			return ReviewHumanRequired, runErr
		}
		contextData["test"] = testResult.Output
		report, passed := testResult.Output.(sandbox.Report)
		summary := attemptSummary(attempt, report)
		history = append(history, summary)
		contextData["attempt_history"] = history
		if passed && report.Applied && report.Passed {
			reviewResult, reviewErr := s.runAgentStep(loopCtx, task, repo, runID, token, domain.TaskReviewing, s.reviewer, contextData, attempt)
			if reviewErr != nil {
				return ReviewHumanRequired, reviewErr
			}
			contextData["reviewer"] = reviewResult.Output
			finalDecision = reviewDecisionFromResult(reviewResult.Output)
			summary["review_decision"] = finalDecision
			contextData["attempt_history"] = history
			if finalDecision == ReviewApprove || finalDecision == ReviewHumanRequired || attempt == maxAttempts {
				break
			}
			contextData["repair_feedback"] = reviewFeedback(reviewResult.Output)
			contextData["repair_instruction"] = "The patch applied and tests passed, but Reviewer requested changes. Regenerate the patch to address every review finding and retain passing tests."
		} else {
			if attempt == maxAttempts {
				reviewResult, reviewErr := s.runAgentStep(loopCtx, task, repo, runID, token, domain.TaskReviewing, s.reviewer, contextData, attempt)
				if reviewErr != nil {
					return ReviewHumanRequired, reviewErr
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
		replan, replanErr := s.runAgentStep(loopCtx, task, repo, runID, token, domain.TaskReplanRequired, s.planner, contextData, attempt+1)
		if replanErr != nil {
			return ReviewHumanRequired, replanErr
		}
		contextData["planner"] = replan.Output
	}
	contextData["repair_attempts"] = len(history)
	return finalDecision, nil
}

func (s *Service) finishWorkflow(task domain.Task, repo domain.Repository, runID string, token int64, finalStatus domain.TaskStatus, finalDecision string, contextData map[string]any, claimed *lease.Lease, updateTask func(domain.TaskStatus, string) error, failRun func(error)) {
	if _, ok := contextData["repair_attempts"]; !ok {
		contextData["repair_attempts"] = 0
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
	if task.MemoryMode != domain.MemoryModeWithout {
		createdMemories, err := s.persistExecutionMemories(repo, task, runID, finalDecision, workflowHistory(contextData), contextData)
		if err != nil {
			failRun(err)
		} else if s.memoryRefiner != nil {
			s.enqueueMemoryRefinement(createdMemories)
		}
	}
	s.finalizeEvaluation(task, finalStatus, contextData)
	if finalStatus == domain.TaskHumanReview {
		s.maybeAutoHandleEvaluationHumanReview(task)
	}
}

func workflowHistory(contextData map[string]any) []map[string]any {
	history, ok := contextData["attempt_history"].([]map[string]any)
	if !ok {
		return []map[string]any{}
	}
	return history
}
