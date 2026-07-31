package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"codecodriver/internal/domain"
	"codecodriver/internal/llm"
	"codecodriver/internal/retrieval"
	"codecodriver/internal/sandbox"
)

type AgentRequest struct {
	Task       domain.Task
	Repository domain.Repository
	Files      []domain.RepositoryFile
	Symbols    []domain.Symbol
	Context    map[string]any
	Attempt    int
}
type AgentResult struct {
	Output                                      any
	ArtifactType, ArtifactName, ArtifactContent string
}
type Agent interface {
	Name() string
	Run(context.Context, AgentRequest) (AgentResult, error)
}

type PlannerAgent struct{ LLM llm.Client }

func (PlannerAgent) Name() string { return "planner" }
func (a PlannerAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	plan := []string{"inspect repository index and prior memory", "retrieve files related to the task", "produce a minimal proposed patch", "run repository validation", "review evidence and risks"}
	if a.LLM != nil {
		prompt := fmt.Sprintf("Repository: %s\nPrimary language: %s\nIndexed files: %d\nIndexed symbols: %d\nTask title: %s\nTask description: %s\n\nCreate a concise, actionable engineering plan. Include retrieval targets, implementation steps, tests, risks, and success criteria. Do not claim to have read file contents.", r.Repository.Name, r.Repository.PrimaryLanguage, len(r.Files), len(r.Symbols), r.Task.Title, r.Task.Description)
		systemPrompt := "You are the Planner Agent in CodeCoDriver. Plan repository changes conservatively and return Markdown."
		if feedback, ok := r.Context["repair_feedback"]; ok {
			encoded, err := json.Marshal(feedback)
			if err != nil {
				return AgentResult{}, fmt.Errorf("encode repair feedback: %w", err)
			}
			prompt += fmt.Sprintf("\n\nThis is repair attempt %d. The previous patch failed validation:\n%s\nCreate a focused repair plan that directly addresses this evidence.", r.Attempt, encoded)
			systemPrompt = "You are the Repair Planner in CodeCoDriver. Use sandbox evidence to plan the smallest correction. Do not repeat a failed approach."
		}
		content, err := a.LLM.Complete(ctx, systemPrompt, prompt)
		if err != nil {
			return AgentResult{}, err
		}
		return AgentResult{Output: map[string]any{"provider": "deepseek", "model": llm.DefaultDeepSeekModel, "plan": content}, ArtifactType: "plan", ArtifactName: "execution-plan.md", ArtifactContent: content}, nil
	}
	return AgentResult{Output: map[string]any{"goal": r.Task.Description, "steps": plan, "success_criteria": []string{"relevant context identified", "validation evidence recorded", "review decision produced"}}, ArtifactType: "plan", ArtifactName: "execution-plan.json", ArtifactContent: strings.Join(plan, "\n")}, nil
}

type CodebaseAgent struct{ Retriever *retrieval.Builder }

func (CodebaseAgent) Name() string { return "codebase" }
func (a CodebaseAgent) Run(_ context.Context, r AgentRequest) (AgentResult, error) {
	terms := tokenize(r.Task.Title + " " + r.Task.Description)
	type scored struct {
		file  domain.RepositoryFile
		score int
	}
	ranked := make([]scored, 0, len(r.Files))
	for _, f := range r.Files {
		hay := strings.ToLower(f.Path + " " + f.Summary)
		score := 0
		for _, t := range terms {
			if strings.Contains(hay, t) {
				score++
			}
		}
		if score > 0 || len(ranked) < 5 {
			ranked = append(ranked, scored{f, score})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > 8 {
		ranked = ranked[:8]
	}
	files := make([]string, 0, len(ranked))
	selected := make([]domain.RepositoryFile, 0, len(ranked))
	for _, item := range ranked {
		files = append(files, item.file.Path)
		selected = append(selected, item.file)
	}
	builder := a.Retriever
	if builder == nil {
		builder = retrieval.New(retrieval.Config{})
	}
	pack := builder.Build(r.Repository, selected)
	return AgentResult{Output: map[string]any{"files": files, "indexed_files": len(r.Files), "indexed_symbols": len(r.Symbols), "context_pack": pack}, ArtifactType: "context", ArtifactName: "context-pack.txt", ArtifactContent: retrieval.Render(pack)}, nil
}

type PatchAgent struct{ LLM llm.Client }

func (PatchAgent) Name() string { return "patch" }
func (a PatchAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	if a.LLM != nil {
		contextJSON, err := json.Marshal(r.Context)
		if err != nil {
			return AgentResult{}, fmt.Errorf("encode agent context: %w", err)
		}
		prompt := fmt.Sprintf("Repository: %s\nTask: %s\nPatch attempt: %d\nPrior agent context:\n%s\n\nPropose the smallest coherent code change. Return one valid unified diff in a diff code fence. Include focused tests when behavior changes. If this is a repair attempt, correct every sandbox error from the previous attempt. Never invent file contents.", r.Repository.Name, r.Task.Description, r.Attempt, contextJSON)
		content, err := a.LLM.Complete(ctx, "You are the Patch Agent in CodeCoDriver. Produce precise, minimal, reviewable changes. The workspace must not be mutated.", prompt)
		if err != nil {
			return AgentResult{}, err
		}
		return AgentResult{Output: map[string]any{"provider": "deepseek", "model": llm.DefaultDeepSeekModel, "mode": "proposal", "mutated_workspace": false, "proposal": content}, ArtifactType: "patch_proposal", ArtifactName: "proposed-change.diff", ArtifactContent: content}, nil
	}
	content := fmt.Sprintf("PROPOSAL ONLY - no files were modified\n\nTask: %s\n\nUse the retrieved context to implement the smallest coherent change, preserve public interfaces, and add focused tests.", r.Task.Description)
	return AgentResult{Output: map[string]any{"mode": "proposal", "mutated_workspace": false, "risk": "requires LLM/tool integration for concrete diff"}, ArtifactType: "patch_proposal", ArtifactName: "proposed-change.txt", ArtifactContent: content}, nil
}

type TestAgent struct{ Sandbox *sandbox.Runner }

func (TestAgent) Name() string { return "test" }
func (a TestAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	proposal, ok := proposalFromContext(r.Context)
	if !ok {
		report := sandbox.Report{Status: "invalid_patch", Error: "patch agent did not produce a proposal"}
		return AgentResult{Output: report, ArtifactType: "test_report", ArtifactName: "sandbox-report.json", ArtifactContent: marshalArtifact(report)}, nil
	}
	runner := a.Sandbox
	if runner == nil {
		runner = sandbox.New(sandbox.Config{})
	}
	report := runner.ValidateAndTest(ctx, r.Repository.Path, proposal)
	return AgentResult{Output: report, ArtifactType: "test_report", ArtifactName: "sandbox-report.json", ArtifactContent: marshalArtifact(report)}, nil
}

type ReviewerAgent struct{ LLM llm.Client }

func (ReviewerAgent) Name() string { return "reviewer" }
func (a ReviewerAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	if a.LLM != nil {
		contextJSON, err := json.Marshal(r.Context)
		if err != nil {
			return AgentResult{}, fmt.Errorf("encode review context: %w", err)
		}
		prompt := fmt.Sprintf("Task: %s\nExecution context including plan, retrieved source, patch proposal, sandbox apply result, and test report:\n%s\n\nReview correctness, missing evidence, regression risk, and test coverage. You MUST NOT approve if the sandbox did not apply the patch or tests did not pass. End with one decision: APPROVE_PROPOSAL, REQUEST_CHANGES, or HUMAN_REVIEW_REQUIRED.", r.Task.Description, contextJSON)
		content, err := a.LLM.Complete(ctx, "You are the Reviewer Agent in CodeCoDriver. Be skeptical, evidence-driven, and concise. Do not approve claims unsupported by the supplied context.", prompt)
		if err != nil {
			return AgentResult{}, err
		}
		return AgentResult{Output: map[string]any{"provider": "deepseek", "model": llm.DefaultDeepSeekModel, "review": content}, ArtifactType: "review", ArtifactName: "review.md", ArtifactContent: content}, nil
	}
	decision := "approved_as_proposal"
	if report, ok := r.Context["test"].(sandbox.Report); ok && (!report.Applied || !report.Passed) {
		decision = "request_changes"
	}
	return AgentResult{Output: map[string]any{"decision": decision, "summary": "Execution completed with an auditable proposal; concrete patch generation remains gated behind an LLM tool."}, ArtifactType: "review", ArtifactName: "review.txt", ArtifactContent: decision}, nil
}

func proposalFromContext(contextData map[string]any) (string, bool) {
	value, ok := contextData["patch"]
	if !ok {
		return "", false
	}
	patch, ok := value.(map[string]any)
	if !ok {
		return "", false
	}
	proposal, ok := patch["proposal"].(string)
	return proposal, ok && strings.TrimSpace(proposal) != ""
}

func marshalArtifact(value any) string {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"error\":%q}", err.Error())
	}
	return string(content)
}

func tokenize(s string) []string {
	seen, out := map[string]bool{}, []string{}
	for _, t := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) {
		if len(t) >= 3 && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
