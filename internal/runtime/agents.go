package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"codecodriver/internal/domain"
	"codecodriver/internal/llm"
	"codecodriver/internal/retrieval"
)

type AgentRequest struct {
	Task       domain.Task
	Repository domain.Repository
	Files      []domain.RepositoryFile
	Symbols    []domain.Symbol
	Context    map[string]any
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
		content, err := a.LLM.Complete(ctx, "You are the Planner Agent in CodeCoDriver. Plan repository changes conservatively and return Markdown.", prompt)
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
		prompt := fmt.Sprintf("Repository: %s\nTask: %s\nPrior agent context:\n%s\n\nPropose the smallest coherent code change. Return a unified diff when context is sufficient. If exact source content is unavailable, state what files must be read before generating a safe diff. Never invent file contents.", r.Repository.Name, r.Task.Description, contextJSON)
		content, err := a.LLM.Complete(ctx, "You are the Patch Agent in CodeCoDriver. Produce precise, minimal, reviewable changes. The workspace must not be mutated.", prompt)
		if err != nil {
			return AgentResult{}, err
		}
		return AgentResult{Output: map[string]any{"provider": "deepseek", "model": llm.DefaultDeepSeekModel, "mode": "proposal", "mutated_workspace": false, "proposal": content}, ArtifactType: "patch_proposal", ArtifactName: "proposed-change.diff", ArtifactContent: content}, nil
	}
	content := fmt.Sprintf("PROPOSAL ONLY - no files were modified\n\nTask: %s\n\nUse the retrieved context to implement the smallest coherent change, preserve public interfaces, and add focused tests.", r.Task.Description)
	return AgentResult{Output: map[string]any{"mode": "proposal", "mutated_workspace": false, "risk": "requires LLM/tool integration for concrete diff"}, ArtifactType: "patch_proposal", ArtifactName: "proposed-change.txt", ArtifactContent: content}, nil
}

type TestAgent struct{}

func (TestAgent) Name() string { return "test" }
func (TestAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	if _, err := os.Stat(filepath.Join(r.Repository.Path, "go.mod")); err != nil {
		return AgentResult{Output: map[string]any{"status": "skipped", "reason": "no supported test runner detected"}}, nil
	}
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = r.Repository.Path
	cmd.Env = append(os.Environ(), "GOTELEMETRY=off")
	out, err := cmd.CombinedOutput()
	result := map[string]any{"command": "go test ./...", "output": string(out), "passed": err == nil}
	if err != nil {
		result["error"] = err.Error()
	}
	return AgentResult{Output: result, ArtifactType: "test_report", ArtifactName: "go-test.txt", ArtifactContent: string(out)}, nil
}

type ReviewerAgent struct{ LLM llm.Client }

func (ReviewerAgent) Name() string { return "reviewer" }
func (a ReviewerAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	if a.LLM != nil {
		contextJSON, err := json.Marshal(r.Context)
		if err != nil {
			return AgentResult{}, fmt.Errorf("encode review context: %w", err)
		}
		prompt := fmt.Sprintf("Task: %s\nExecution context including plan, retrieved files, patch proposal metadata, and test report:\n%s\n\nReview correctness, missing evidence, regression risk, and test coverage. End with one decision: APPROVE_PROPOSAL, REQUEST_CHANGES, or HUMAN_REVIEW_REQUIRED.", r.Task.Description, contextJSON)
		content, err := a.LLM.Complete(ctx, "You are the Reviewer Agent in CodeCoDriver. Be skeptical, evidence-driven, and concise. Do not approve claims unsupported by the supplied context.", prompt)
		if err != nil {
			return AgentResult{}, err
		}
		return AgentResult{Output: map[string]any{"provider": "deepseek", "model": llm.DefaultDeepSeekModel, "review": content}, ArtifactType: "review", ArtifactName: "review.md", ArtifactContent: content}, nil
	}
	decision := "approved_as_proposal"
	return AgentResult{Output: map[string]any{"decision": decision, "summary": "Execution completed with an auditable proposal; concrete patch generation remains gated behind an LLM tool."}, ArtifactType: "review", ArtifactName: "review.txt", ArtifactContent: decision}, nil
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
