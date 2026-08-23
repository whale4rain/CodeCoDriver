package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"codecodriver/internal/llm"
)

type OrchestratorAgent struct{ LLM llm.Client }

func (OrchestratorAgent) Name() string { return "orchestrator" }

func (a OrchestratorAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	plan := "no plan generated"
	if value, ok := r.Context["planner"].(map[string]any); ok {
		if text, ok := value["plan"].(string); ok && strings.TrimSpace(text) != "" {
			plan = text
		}
	}
	prompt := fmt.Sprintf("Repository: %s\nTask title: %s\nTask description: %s\n\nPlanner output:\n%s\n\nChoose the next workflow step for this task. Return JSON only with these fields:\n{\"decision\":\"code_change|explain|request_human\",\"next_step\":\"codebase|finish\",\"target\":\"patch_loop|explainer\",\"reason\":\"short rationale\"}\n\nUse code_change for engineering changes, explain for read-only explanation, and request_human when the task is ambiguous or needs user input. Do not call tools.", r.Repository.Name, r.Task.Title, r.Task.Description, plan)
	systemPrompt := "You are the Workflow Orchestrator in CodeCoDriver. Choose the next agent or human decision based on the planner output and the task. Be conservative and return valid JSON only."
	prompt, systemPrompt, _, err := applySkillPrompt(r, "orchestrator", prompt, systemPrompt)
	if err != nil {
		return AgentResult{}, err
	}
	if feedback := humanFeedbackContext(r); feedback != "" {
		prompt += feedback
	}
	if a.LLM != nil {
		content, err := a.LLM.Complete(ctx, systemPrompt, prompt)
		if err != nil {
			return AgentResult{}, err
		}
		decision := parseWorkflowDecisionText(content)
		if decision.Next == "" {
			decision = WorkflowDecision{Decision: "code_change", Next: "codebase", Target: "patch_loop", Reason: "orchestrator output was not valid JSON; defaulted to code change"}
		}
		return AgentResult{
			Output:          decision,
			ArtifactType:    "workflow_decision",
			ArtifactName:    "workflow-decision.json",
			ArtifactContent: marshalArtifact(decision),
		}, nil
	}
	decision := WorkflowDecision{Decision: "code_change", Next: "codebase", Target: "patch_loop", Reason: "orchestrator LLM is not configured"}
	return AgentResult{
		Output:          decision,
		ArtifactType:    "workflow_decision",
		ArtifactName:    "workflow-decision.json",
		ArtifactContent: marshalArtifact(decision),
	}, nil
}

func parseWorkflowDecision(output any) WorkflowDecision {
	switch value := output.(type) {
	case WorkflowDecision:
		return value
	case map[string]any:
		var decision WorkflowDecision
		decision.Decision, _ = value["decision"].(string)
		decision.Next, _ = value["next_step"].(string)
		if decision.Next == "" {
			decision.Next, _ = value["next"].(string)
		}
		decision.Target, _ = value["target"].(string)
		decision.Reason, _ = value["reason"].(string)
		return decision
	case string:
		return parseWorkflowDecisionText(value)
	default:
		return WorkflowDecision{}
	}
}

func parseWorkflowDecisionText(content string) WorkflowDecision {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	var decision WorkflowDecision
	if json.Unmarshal([]byte(content), &decision) == nil && decision.Next != "" {
		return decision
	}
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		if json.Unmarshal([]byte(content[start:end+1]), &decision) == nil && decision.Next != "" {
			return decision
		}
	}
	return WorkflowDecision{}
}
