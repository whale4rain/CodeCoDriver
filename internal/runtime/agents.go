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
	"codecodriver/internal/tools"
)

type AgentRequest struct {
	Task       domain.Task
	Repository domain.Repository
	Files      []domain.RepositoryFile
	Symbols    []domain.Symbol
	Artifacts  []domain.Artifact
	Context    map[string]any
	Attempt    int
	Tools      *tools.Gateway
	Workspace  sandbox.Workspace
}
type AgentResult struct {
	Output                                      any
	ArtifactType, ArtifactName, ArtifactContent string
}
type Agent interface {
	Name() string
	Run(context.Context, AgentRequest) (AgentResult, error)
}

const (
	ReviewApprove        = "APPROVE_PROPOSAL"
	ReviewRequestChanges = "REQUEST_CHANGES"
	ReviewHumanRequired  = "HUMAN_REVIEW_REQUIRED"

	PlannerSkipDecision = "SKIP_SUGGESTED"
)

type PlannerAgent struct{ LLM llm.Client }

func (PlannerAgent) Name() string { return "planner" }
func (a PlannerAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	if skip, ok := suggestPlannerSkip(r); ok {
		return AgentResult{Output: skip, ArtifactType: "planner_skip", ArtifactName: "planner-skip.json", ArtifactContent: marshalArtifact(skip)}, nil
	}
	plan := []string{"inspect repository index and prior memory", "retrieve files related to the task", "produce a minimal proposed patch", "run repository validation", "review evidence and risks"}
	if a.LLM != nil {
		prompt := fmt.Sprintf("Repository: %s\nPrimary language: %s\nIndexed files: %d\nIndexed symbols: %d\nTask title: %s\nTask description: %s\n\nCreate a concise, actionable engineering plan. Include retrieval targets, implementation steps, tests, risks, and success criteria. Do not claim to have read file contents.", r.Repository.Name, r.Repository.PrimaryLanguage, len(r.Files), len(r.Symbols), r.Task.Title, r.Task.Description)
		systemPrompt := "You are the Planner Agent in CodeCoDriver. Plan repository changes conservatively and return Markdown."
		prompt, systemPrompt, skillApplied, err := applySkillPrompt(r, "planner", prompt, systemPrompt)
		if err != nil {
			return AgentResult{}, err
		}
		if memories, ok := r.Context["memory"].([]domain.MemoryEntry); ok {
			if guidance := memoryGuidance(memories); guidance != "" {
				prompt += "\n\nHistorical repository memory (use as evidence, not as ground truth):\n" + guidance + "\nPrefer approaches that match verified success patterns and avoid repeating known failure patterns."
			}
		}
		if !skillApplied && documentationTask(r.Task) {
			prompt += "\n\nDOCUMENTATION TASK: This is a documentation-only change. Locate the existing README or markdown file, verify claims against the context, and do not invent endpoints, commands, or license details that are not present."
		}
		if feedback := humanFeedbackContext(r); feedback != "" {
			prompt += feedback
		}
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

type fileScore struct {
	file  domain.RepositoryFile
	score int
}

func (CodebaseAgent) Name() string { return "codebase" }
func (a CodebaseAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	if r.Workspace == nil {
		return AgentResult{}, fmt.Errorf("codebase agent requires an isolated workspace")
	}
	terms := tokenize(r.Task.Title + " " + r.Task.Description)
	memories, _ := r.Context["memory"].([]domain.MemoryEntry)
	primary := primaryTaskToken(r.Files, r.Task.Title)
	wantsTests := wantsTestCoverage(terms)
	memoryFiles := map[string]bool{}
	memorySymbols := map[string]bool{}
	symbolTerms := map[string]bool{}
	for _, memory := range memories {
		for _, path := range memory.ChangedFiles {
			memoryFiles[strings.ToLower(path)] = true
		}
		for _, symbol := range memory.Symbols {
			memorySymbols[symbol] = true
		}
	}
	for _, term := range terms {
		symbolTerms[term] = true
	}
	ranked := make([]fileScore, 0, len(r.Files))
	for _, f := range r.Files {
		lowerPath := strings.ToLower(f.Path)
		hay := lowerPath + " " + strings.ToLower(f.Summary)
		score := 0
		for _, t := range terms {
			if strings.Contains(lowerPath, t) {
				score += 2
				if t == primary {
					score += 4
				}
			} else if strings.Contains(hay, t) {
				score++
			}
		}
		if wantsTests && strings.HasSuffix(f.Path, "_test.go") {
			score += 2
		}
		if memoryFiles[lowerPath] {
			score += 3
		}
		if fileHasMemorySymbol(f.Path, r.Symbols, memorySymbols) {
			score += 2
		}
		if fileHasTermSymbol(f.Path, r.Symbols, symbolTerms) {
			score += 4
		}
		if score > 0 && (f.Language != "" && f.Language != "markdown") {
			ranked = append(ranked, fileScore{f, score})
		}
	}
	if len(ranked) == 0 {
		for _, f := range r.Files {
			if f.Language == "" || f.Language == "markdown" {
				continue
			}
			ranked = append(ranked, fileScore{f, 0})
			if len(ranked) >= 5 {
				break
			}
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].file.Path < ranked[j].file.Path
	})
	files := skillPathFiles(r)
	remaining := 8
	files = append(files, selectContextFiles(ranked, wantsTests, remaining-len(files))...)
	if documentationTask(r.Task) {
		for _, file := range r.Files {
			if isDocumentationFile(file.Path) {
				files = appendUniquePath(files, file.Path)
			}
			if len(files) >= remaining {
				break
			}
		}
	}
	if wantsTests {
		for _, file := range r.Files {
			if isTestHelperPath(file.Path) {
				files = appendUniquePath(files, file.Path)
			}
			if len(files) >= remaining {
				break
			}
		}
	}
	byPath := make(map[string]domain.RepositoryFile, len(r.Files))
	for _, f := range r.Files {
		byPath[f.Path] = f
	}
	selected := make([]domain.RepositoryFile, 0, len(files))
	for _, path := range files {
		if f, ok := byPath[path]; ok {
			selected = append(selected, f)
		}
	}
	selectedSymbols := symbolsForFiles(r.Symbols, files)
	builder := a.Retriever
	if builder == nil {
		config := retrieval.Config{}
		if wantsTests {
			config.MaxFiles = 8
			config.MaxTotalBytes = 48 * 1024
		}
		builder = retrieval.New(config)
	}
	pack := builder.Build(ctx, selected, r.Workspace)
	return AgentResult{Output: map[string]any{"files": files, "symbols": selectedSymbols, "indexed_files": len(r.Files), "indexed_symbols": len(r.Symbols), "memory_hits": len(memories), "context_pack": pack, "context_pack_text": retrieval.Render(pack)}, ArtifactType: "context", ArtifactName: "context-pack.txt", ArtifactContent: retrieval.Render(pack)}, nil
}

func primaryTaskToken(files []domain.RepositoryFile, title string) string {
	for _, term := range tokenize(title) {
		for _, file := range files {
			if strings.Contains(strings.ToLower(file.Path), term) {
				return term
			}
		}
	}
	return ""
}

func wantsTestCoverage(terms []string) bool {
	for _, term := range terms {
		if strings.HasPrefix(term, "test") || term == "coverage" || term == "spec" || term == "case" {
			return true
		}
	}
	return false
}

func documentationTask(task domain.Task) bool {
	text := strings.ToLower(task.Title + " " + task.Description)
	for _, marker := range []string{"readme", "reamdme", "中文文档", "文档", "documentation", "document", "markdown", "docs", ".md"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isDocumentationFile(path string) bool {
	lower := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	return strings.HasSuffix(lower, ".md") || strings.Contains(lower, "/docs/")
}

func selectContextFiles(ranked []fileScore, wantsTests bool, max int) []string {
	selected := make([]string, 0, max)
	seen := make(map[string]bool, max)
	add := func(path string) {
		if len(selected) >= max || seen[path] {
			return
		}
		seen[path] = true
		selected = append(selected, path)
	}
	for _, item := range ranked {
		if len(selected) >= 5 {
			break
		}
		add(item.file.Path)
		if wantsTests {
			if strings.HasSuffix(item.file.Path, "_test.go") {
				add(strings.TrimSuffix(item.file.Path, "_test.go") + ".go")
			} else if strings.HasSuffix(item.file.Path, ".go") {
				add(strings.TrimSuffix(item.file.Path, ".go") + "_test.go")
			}
		}
	}
	return selected
}

func appendUniquePath(paths []string, path string) []string {
	for _, existing := range paths {
		if existing == path {
			return paths
		}
	}
	return append(paths, path)
}

func isTestHelperPath(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	return strings.Contains(normalized, "/test/") || strings.HasSuffix(normalized, "/test")
}

func fileHasTermSymbol(path string, symbols []domain.Symbol, terms map[string]bool) bool {
	if len(terms) == 0 {
		return false
	}
	for _, symbol := range symbols {
		if symbol.FilePath != path {
			continue
		}
		lower := strings.ToLower(symbol.Name)
		for term := range terms {
			if lower == term || strings.Contains(lower, term) || strings.Contains(term, lower) {
				return true
			}
		}
	}
	return false
}

type PatchAgent struct{ LLM llm.Client }

func (PatchAgent) Name() string { return "patch" }
func (a PatchAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	if a.LLM != nil {
		contextJSON, err := leanContextJSON(r.Context)
		if err != nil {
			return AgentResult{}, fmt.Errorf("encode agent context: %w", err)
		}
		r.Context["context_json"] = string(contextJSON)
		editMode := r.Workspace != nil
		prompt := fmt.Sprintf("Repository: %s\nTask: %s\nPatch attempt: %d\nPrior agent context:\n%s\n\nPropose the smallest coherent code change. Include focused tests when behavior changes. Correct every sandbox error. Never invent or omit source context.", r.Repository.Name, r.Task.Description, r.Attempt, contextJSON)
		if source := contextPackTextFromContext(r.Context); source != "" {
			prompt += "\n\nRETRIEVED SOURCE:\n" + retrievedSourceForPrompt(source)
		}
		systemPrompt := "You are the Patch Agent in CodeCoDriver. Produce precise, minimal, reviewable changes. The workspace must not be mutated."
		if editMode {
			systemPrompt = "You are the Patch Agent in CodeCoDriver. Modify files only inside the isolated workspace and generate the resulting patch."
		}
		prompt, systemPrompt, skillApplied, err := applySkillPrompt(r, "patch", prompt, systemPrompt)
		if err != nil {
			return AgentResult{}, err
		}
		if !skillApplied && documentationTask(r.Task) {
			if editMode {
				prompt += "\n\nDOCUMENTATION TASK: This is a documentation-only change. If README.md or another .md file already appears in context_pack, edit that existing file with edit_file/write_file. Never create a duplicate README. Do not change production code."
			} else {
				prompt += "\n\nDOCUMENTATION TASK: This is a documentation-only change. If README.md or another .md file already appears in context_pack, modify that existing file with `--- a/<path>`; never create it with `--- /dev/null`. Do not change production code. Tests are not required for approval, but the diff must still apply cleanly."
			}
		}
		if memories, ok := r.Context["memory"].([]domain.MemoryEntry); ok {
			if guidance := memoryGuidance(memories); guidance != "" {
				prompt += "\n\n" + guidance + "\nMemory contract: if a failure_pattern matches the current sandbox error, do not repeat the failed approach; if an execution_success applies, reuse the validated files and approach."
			}
		}
		if feedback := humanFeedbackContext(r); feedback != "" {
			prompt += feedback
		}
		if r.Attempt > 1 {
			prompt += "\n\nREPAIR STATE: Every attempt starts from the ORIGINAL repository. Previous patches were applied only in disposable sandboxes and then discarded. Produce a complete standalone diff against the current context_pack, not an incremental patch to code introduced by an earlier attempt. Do not reference functions, files, or line ranges added by previous attempts."
		}
		prompt += "\n\nTASK CONTRACT: If the task asks to harden, validate, or change runtime behavior, the diff must change production code accordingly. Tests that only document unchanged behavior are not sufficient. Add focused tests that exercise the new behavior, including timeout, cancellation, invalid input, or degraded-state scenarios when they are part of the task."
		prompt += "\n\nHTTP TEST CONTRACT: When writing HTTP endpoint tests, derive expected status, headers, and body from the exact handler and existing test helper visible in context_pack. Do not assume HEAD has an empty body, GET returns JSON, or an unsupported method returns 405 unless the retrieved source proves it."
		prompt += "\n\nTEST EXPECTATION CONTRACT: Before writing assertions, trace every input through the current implementation. Expected values must be the final post-clamp, post-parse, post-normalization values, not pre-processing intuition. If the task requires preserving production behavior and a test fails, treat the actual sandbox output as the likely correct expected value unless the production behavior is clearly a bug."
		prompt += "\n\nTOOL GROUNDING: Before editing a file, use read_file on every file you plan to modify and copy exact whitespace and context lines from the tool result. Use search_files or read_symbols when context_pack lacks a file or symbol."
		prompt += "\n\nTEST HELPER CONTRACT: Only use functions and types that are visible in context_pack. Never invent helpers such as test.DoRequest or test.PerformRequest. When the context includes internal/test helper files, reuse the exact helper signatures shown there. Prefer adding new focused test functions at the end of an existing *_test.go file instead of rewriting existing tests."
		if !editMode {
			prompt += "\n\nDIFF RULES: Every FILE section in context_pack is a file that already exists. Begin every changed file with `diff --git a/<path> b/<path>` before its `---`/`+++` headers. For an existing file, use `--- a/<path>` and `+++ b/<path>` with exact unchanged context lines and never create it again. For a genuinely new file, use `--- /dev/null`, `+++ b/<path>`, and `new file mode 100644`. Prefer modifying an existing *_test.go file when the context pack includes one. Every hunk must end with at least one unchanged context line after the last `+`/`-` line. The line-number prefix in context_pack is display-only; the real file lines do not contain the `N |` prefix. The extracted diff must contain no markdown, no prose, and no extra code fences."
			prompt += "\n\nOUTPUT CONTRACT: Return exactly one ```diff code fence containing the complete unified diff. Do not emit analysis, file-read requests, tool calls, multiple diffs, or any prose outside the fence. If you need source evidence, use the FILE sections already present in context_pack."
			prompt += "\n\nHUNK CONTEXT CONTRACT: Copy unchanged context lines exactly from context_pack. Do not paraphrase, reorder, or include lines that are not present. If a previous sandbox error says a patch does not apply or a hunk is stale, regenerate the hunk against the exact current context_pack and do not reuse old hunk headers."
			prompt += "\n\nEOF CONTRACT: A file must end with exactly one newline. Never add an extra blank `+` line at EOF. If the sandbox reports `new blank line at EOF` or `adds whitespace errors`, remove that trailing empty line and regenerate the diff."
		}
		var content string
		if editMode {
			patchTools := toolAllowList("read_file", "search_files", "read_symbols", "edit_file", "write_file", "generate_patch")
			prompt += "\n\nEDIT MODE CONTRACT: You are editing a disposable sandbox copy, not the real repository. Do NOT return a unified diff. Inspect exact file content with read_file/search_files/read_symbols. Change files only with edit_file or write_file. After all edits, call generate_patch. A final answer without a tool call is allowed only after generate_patch has returned the patch; otherwise continue calling tools."
			prompt += "\n\nEDIT CALL CONTRACT: Prefer edit_file with old_string/new_string for existing files. If you use content/start/end, the content must exactly replace that line range and must not insert a line that already exists elsewhere. Re-read the file after every edit and regenerate the patch; do not reuse stale line numbers."
			prompt += agentToolInstructions(patchTools)
			var loopErr error
			content, loopErr = runPatchEditLoop(ctx, r, a.LLM, systemPrompt, prompt, patchTools)
			if loopErr != nil {
				return AgentResult{}, loopErr
			}
		} else {
			patchTools := toolAllowList("read_file", "search_files", "read_symbols")
			prompt += agentToolInstructions(patchTools)
			var loopErr error
			content, loopErr = runAgentToolLoop(ctx, r, a.LLM, systemPrompt, prompt, patchTools)
			if loopErr != nil {
				return AgentResult{}, loopErr
			}
			if _, preflightErr := sandbox.PreflightDiff(content); preflightErr != nil {
				retryPrompt := "Your previous response failed diff preflight: " + preflightErr.Error() + ". Return only one ```diff code fence containing a structurally valid complete diff. Do not emit analysis, file reads, tool calls, or prose."
				if repaired, retryErr := a.LLM.Complete(ctx, "You are the Patch Agent in CodeCoDriver. Return a complete unified diff only.", retryPrompt); retryErr == nil {
					if _, retryPreflightErr := sandbox.PreflightDiff(repaired); retryPreflightErr == nil {
						content = repaired
					}
				}
			}
		}
		return AgentResult{Output: map[string]any{"provider": "deepseek", "model": llm.DefaultDeepSeekModel, "mode": "proposal", "mutated_workspace": editMode, "proposal": content}, ArtifactType: "patch_proposal", ArtifactName: "proposed-change.diff", ArtifactContent: content}, nil
	}
	content := fmt.Sprintf("PROPOSAL ONLY - no files were modified\n\nTask: %s\n\nUse the retrieved context to implement the smallest coherent change, preserve public interfaces, and add focused tests.", r.Task.Description)
	return AgentResult{Output: map[string]any{"mode": "proposal", "mutated_workspace": false, "risk": "requires LLM/tool integration for concrete diff"}, ArtifactType: "patch_proposal", ArtifactName: "proposed-change.txt", ArtifactContent: content}, nil
}

type TestAgent struct{}

func (TestAgent) Name() string { return "test" }
func (a TestAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	if r.Workspace != nil {
		_, ok := proposalFromContext(r.Context)
		if !ok {
			report := sandbox.Report{Status: "invalid_patch", Error: "patch agent did not produce a proposal"}
			return AgentResult{Output: report, ArtifactType: "test_report", ArtifactName: "sandbox-report.json", ArtifactContent: marshalArtifact(report)}, nil
		}
		testCommand := r.Repository.TestCommand
		if override, ok := r.Context["test_command_override"].(string); ok && strings.TrimSpace(override) != "" {
			testCommand = strings.TrimSpace(override)
		}
		report := r.Workspace.RunTest(ctx, testCommand)
		if documentationTask(r.Task) && report.Applied {
			report.Passed = true
			report.Status = "passed"
			if strings.TrimSpace(report.Output) == "" {
				report.Output = "documentation-only task: patch applied successfully; test execution not required"
			} else {
				report.Output += "\n\ndocumentation-only task: patch applied successfully; test execution not required"
			}
		}
		return AgentResult{Output: report, ArtifactType: "test_report", ArtifactName: "sandbox-report.json", ArtifactContent: marshalArtifact(report)}, nil
	}
	report := sandbox.Report{Status: "invalid_patch", Error: "test agent requires an isolated workspace; host repository paths are not allowed"}
	return AgentResult{Output: report, ArtifactType: "test_report", ArtifactName: "sandbox-report.json", ArtifactContent: marshalArtifact(report)}, nil
}

type ReviewerAgent struct{ LLM llm.Client }

func (ReviewerAgent) Name() string { return "reviewer" }
func (a ReviewerAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	if a.LLM != nil {
		contextJSON, err := leanContextJSON(r.Context)
		if err != nil {
			return AgentResult{}, fmt.Errorf("encode review context: %w", err)
		}
		r.Context["context_json"] = string(contextJSON)
		prompt := fmt.Sprintf("Task: %s\nExecution context including plan, retrieved source, patch proposal, sandbox apply result, and test report:\n%s\n\nReview correctness, missing evidence, regression risk, and test coverage. You MUST NOT approve if the sandbox did not apply the patch or tests did not pass. End with one decision: APPROVE_PROPOSAL, REQUEST_CHANGES, or HUMAN_REVIEW_REQUIRED.", r.Task.Description, contextJSON)
		if source := contextPackTextFromContext(r.Context); source != "" {
			prompt += "\n\nRETRIEVED SOURCE:\n" + retrievedSourceForPrompt(source)
		}
		prompt, systemPrompt, skillApplied, err := applySkillPrompt(r, "reviewer", prompt, "You are the Reviewer Agent in CodeCoDriver. Be skeptical, evidence-driven, and concise. Do not approve claims unsupported by the supplied context.")
		if err != nil {
			return AgentResult{}, err
		}
		if !skillApplied && documentationTask(r.Task) {
			prompt += "\n\nDOCUMENTATION TASK: This is a documentation-only change. If the patch applied successfully and the documentation is consistent with context_pack, approve without requiring test output."
		}
		if memories, ok := r.Context["memory"].([]domain.MemoryEntry); ok {
			if guidance := memoryGuidance(memories); guidance != "" {
				prompt += "\n\n" + guidance + "\nMemory contract: cross-check the proposal against known success patterns and verify it does not repeat known failure patterns."
			}
		}
		if feedback := humanFeedbackContext(r); feedback != "" {
			prompt += feedback
		}
		reviewTools := toolAllowList("read_file", "search_files", "read_symbols")
		prompt += agentToolInstructions(reviewTools)
		content, err := runAgentToolLoop(ctx, r, a.LLM, systemPrompt, prompt, reviewTools)
		if err != nil {
			return AgentResult{}, err
		}
		decision := parseReviewDecision(content)
		return AgentResult{Output: map[string]any{"provider": "deepseek", "model": llm.DefaultDeepSeekModel, "decision": decision, "review": content}, ArtifactType: "review", ArtifactName: "review.md", ArtifactContent: content}, nil
	}
	decision := ReviewApprove
	if report, ok := r.Context["test"].(sandbox.Report); ok && (!report.Applied || !report.Passed) {
		decision = ReviewRequestChanges
	}
	return AgentResult{Output: map[string]any{"decision": decision, "summary": "Execution completed with an auditable proposal; concrete patch generation remains gated behind an LLM tool."}, ArtifactType: "review", ArtifactName: "review.txt", ArtifactContent: decision}, nil
}

func parseReviewDecision(content string) string {
	upper := strings.ToUpper(content)
	lines := strings.Split(upper, "\n")
	candidates := []string{ReviewApprove, ReviewRequestChanges, ReviewHumanRequired}
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.Trim(lines[i], " \t#*:-.")
		for _, candidate := range candidates {
			if line == candidate || line == "DECISION: "+candidate {
				return candidate
			}
		}
	}
	lastDecision, lastIndex := ReviewHumanRequired, -1
	for _, candidate := range candidates {
		if index := strings.LastIndex(upper, candidate); index > lastIndex {
			lastDecision, lastIndex = candidate, index
		}
	}
	return lastDecision
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

func memoryGuidance(memories []domain.MemoryEntry) string {
	if len(memories) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, memory := range memories {
		kind := memory.Kind
		if kind == "" {
			kind = "memory"
		}
		detail := strings.TrimSpace(memory.Summary)
		if detail == "" {
			detail = strings.TrimSpace(memory.Content)
		}
		if memory.Symptom != "" {
			detail += "; symptom=" + memory.Symptom
		}
		if memory.RootCause != "" {
			detail += "; root_cause=" + memory.RootCause
		}
		if len(memory.ChangedFiles) > 0 {
			detail += "; files=" + strings.Join(memory.ChangedFiles, ",")
		}
		if len(memory.Symbols) > 0 {
			detail += "; symbols=" + strings.Join(memory.Symbols, ",")
		}
		fmt.Fprintf(&builder, "- %s [score %.2f]: %s\n", kind, memory.Score, truncateMemoryText(detail, 800))
	}
	return builder.String()
}

func truncateMemoryText(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "..."
}

const maxRetrievedSourcePromptBytes = 48 * 1024

func retrievedSourceForPrompt(source string) string {
	if len(source) <= maxRetrievedSourcePromptBytes {
		return source
	}
	return source[:maxRetrievedSourcePromptBytes] + "\n[SOURCE TRUNCATED]"
}

func fileHasMemorySymbol(path string, symbols []domain.Symbol, wanted map[string]bool) bool {
	if len(wanted) == 0 {
		return false
	}
	for _, symbol := range symbols {
		if symbol.FilePath == path && wanted[symbol.Name] {
			return true
		}
	}
	return false
}

func symbolsForFiles(symbols []domain.Symbol, files []string) []string {
	wanted := make(map[string]bool, len(files))
	for _, file := range files {
		wanted[file] = true
	}
	seen := map[string]bool{}
	out := []string{}
	for _, symbol := range symbols {
		if !wanted[symbol.FilePath] || seen[symbol.Name] {
			continue
		}
		seen[symbol.Name] = true
		out = append(out, symbol.Name)
		if len(out) >= 50 {
			break
		}
	}
	return out
}
