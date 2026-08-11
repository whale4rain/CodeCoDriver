package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"codecodriver/internal/domain"
)

type evalReport struct {
	GeneratedAt time.Time                    `json:"generated_at"`
	Summary     evalReportSummary            `json:"summary"`
	Categories  map[string]evalCategoryStats `json:"categories"`
	AgentStats  map[string]evalAgentUsage    `json:"agent_stats"`
	ToolStats   map[string]evalToolUsage     `json:"tool_stats"`
	Runs        []evalRunReport              `json:"runs"`
}

type evalReportSummary struct {
	TotalRuns      int     `json:"total_runs"`
	Passed         int     `json:"passed"`
	Failed         int     `json:"failed"`
	ExternalErrors int     `json:"external_errors"`
	AutoHuman      int     `json:"auto_human"`
	AvgQuality     float64 `json:"avg_quality"`
	AvgTokens      int     `json:"avg_tokens"`
	AvgCostUSD     float64 `json:"avg_cost_usd"`
	AvgDurationMS  int64   `json:"avg_duration_ms"`
}

type evalCategoryStats struct {
	Total         int     `json:"total"`
	Passed        int     `json:"passed"`
	Failed        int     `json:"failed"`
	External      int     `json:"external_errors"`
	AvgQuality    float64 `json:"avg_quality"`
	AvgTokens     int     `json:"avg_tokens"`
	AvgCostUSD    float64 `json:"avg_cost_usd"`
	AvgDurationMS int64   `json:"avg_duration_ms"`
}

type evalRunReport struct {
	RunID          string                    `json:"run_id"`
	TaskID         string                    `json:"task_id"`
	CaseID         string                    `json:"case_id"`
	CaseName       string                    `json:"case_name"`
	Category       string                    `json:"category"`
	Mode           string                    `json:"mode"`
	Status         string                    `json:"status"`
	Passed         bool                      `json:"passed"`
	CreatedAt      time.Time                 `json:"created_at"`
	DurationMS     int64                     `json:"duration_ms"`
	QualityScore   float64                   `json:"quality_score"`
	ScoreBreakdown map[string]float64        `json:"score_breakdown"`
	TokenUsage     evalTokenUsage            `json:"token_usage"`
	Agents         map[string]evalAgentUsage `json:"agents"`
	ToolUsage      map[string]evalToolUsage  `json:"tool_usage"`
	Dimensions     map[string]evalDimension  `json:"dimensions"`
	Trace          evalTraceAnalysis         `json:"trace"`
	RepairAttempts int                       `json:"repair_attempts"`
	MemoryHits     int                       `json:"memory_hits"`
	Artifacts      evalArtifactStats         `json:"artifacts"`
	ChangedFiles   []string                  `json:"changed_files,omitempty"`
	ExpectedPaths  []string                  `json:"expected_paths,omitempty"`
	ExternalError  bool                      `json:"external_error"`
}

type evalTokenUsage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

type evalAgentUsage struct {
	Calls            int     `json:"calls"`
	Steps            int     `json:"steps"`
	ToolCalls        int     `json:"tool_calls"`
	ToolErrors       int     `json:"tool_errors"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	LatencyMS        int64   `json:"latency_ms"`
	AvgLatencyMS     int64   `json:"avg_latency_ms"`
}

type evalToolUsage struct {
	Calls        int   `json:"calls"`
	Errors       int   `json:"errors"`
	LatencyMS    int64 `json:"latency_ms"`
	AvgLatencyMS int64 `json:"avg_latency_ms"`
}

type evalDimension struct {
	Score   float64        `json:"score"`
	Max     float64        `json:"max"`
	Label   string         `json:"label"`
	Details map[string]any `json:"details,omitempty"`
}

type evalTraceAnalysis struct {
	Phases map[string]evalPhaseStats `json:"phases"`
	Events []evalTraceEvent          `json:"events"`
}

type evalPhaseStats struct {
	Calls      int   `json:"calls"`
	Tokens     int   `json:"tokens"`
	ToolCalls  int   `json:"tool_calls"`
	ToolErrors int   `json:"tool_errors"`
	LatencyMS  int64 `json:"latency_ms"`
	LLMCalls   int   `json:"llm_calls"`
}

type evalTraceEvent struct {
	ID               string    `json:"id"`
	Type             string    `json:"type"`
	Agent            string    `json:"agent,omitempty"`
	Phase            string    `json:"phase,omitempty"`
	Attempt          int       `json:"attempt,omitempty"`
	Status           string    `json:"status,omitempty"`
	Label            string    `json:"label,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	LatencyMS        int64     `json:"latency_ms,omitempty"`
	PromptTokens     int       `json:"prompt_tokens,omitempty"`
	CompletionTokens int       `json:"completion_tokens,omitempty"`
	TotalTokens      int       `json:"total_tokens,omitempty"`
	EstimatedCostUSD float64   `json:"estimated_cost_usd,omitempty"`
	ToolCalls        int       `json:"tool_calls,omitempty"`
	ToolErrors       int       `json:"tool_errors,omitempty"`
	Summary          string    `json:"summary,omitempty"`
}

type evalArtifactStats struct {
	Count            int    `json:"count"`
	PatchBytes       int    `json:"patch_bytes"`
	ExplanationChars int    `json:"explanation_chars"`
	PatchText        string `json:"-"`
	ExplanationText  string `json:"-"`
}

func (s *Server) evaluationReport(w http.ResponseWriter, r *http.Request) {
	cases, err := s.store.BenchmarkCases()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	runs, err := s.store.AllEvaluationRuns()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	caseByID := map[string]domain.BenchmarkCase{}
	for _, item := range cases {
		caseByID[item.ID] = item
	}
	reports := []evalRunReport{}
	summary := evalReportSummary{}
	categories := map[string]evalCategoryStats{}
	for _, run := range runs {
		if batchID := r.URL.Query().Get("batch_id"); batchID != "" && run.BatchID != batchID {
			continue
		}
		report := s.buildEvalRunReport(run, caseByID[run.CaseID])
		reports = append(reports, report)
		summary.TotalRuns++
		summary.AutoHuman += evalAutoHumanCount(run.Notes)
		summary.ExternalErrors += boolInt(report.ExternalError)
		if report.Passed {
			summary.Passed++
		} else if !report.ExternalError {
			summary.Failed++
		}
		summary.AvgQuality += report.QualityScore
		summary.AvgTokens += report.TokenUsage.TotalTokens
		summary.AvgCostUSD += report.TokenUsage.EstimatedCostUSD
		summary.AvgDurationMS += report.DurationMS

		category := categories[report.Category]
		category.Total++
		category.External += boolInt(report.ExternalError)
		if report.Passed {
			category.Passed++
		} else if !report.ExternalError {
			category.Failed++
		}
		category.AvgQuality += report.QualityScore
		category.AvgTokens += report.TokenUsage.TotalTokens
		category.AvgCostUSD += report.TokenUsage.EstimatedCostUSD
		category.AvgDurationMS += report.DurationMS
		categories[report.Category] = category
	}
	if summary.TotalRuns > 0 {
		summary.AvgQuality = summary.AvgQuality / float64(summary.TotalRuns)
		summary.AvgTokens = summary.AvgTokens / summary.TotalRuns
		summary.AvgCostUSD = summary.AvgCostUSD / float64(summary.TotalRuns)
		summary.AvgDurationMS = summary.AvgDurationMS / int64(summary.TotalRuns)
	}
	for key, category := range categories {
		if category.Total > 0 {
			category.AvgQuality = category.AvgQuality / float64(category.Total)
			category.AvgTokens = category.AvgTokens / category.Total
			category.AvgCostUSD = category.AvgCostUSD / float64(category.Total)
			category.AvgDurationMS = category.AvgDurationMS / int64(category.Total)
		}
		categories[key] = category
	}
	sort.SliceStable(reports, func(i, j int) bool { return reports[i].CreatedAt.Before(reports[j].CreatedAt) })
	agentStats := map[string]evalAgentUsage{}
	for _, report := range reports {
		for name, agent := range report.Agents {
			total := agentStats[name]
			total.Calls += agent.Calls
			total.Steps += agent.Steps
			total.ToolCalls += agent.ToolCalls
			total.ToolErrors += agent.ToolErrors
			total.PromptTokens += agent.PromptTokens
			total.CompletionTokens += agent.CompletionTokens
			total.TotalTokens += agent.TotalTokens
			total.EstimatedCostUSD += agent.EstimatedCostUSD
			total.LatencyMS += agent.LatencyMS
			if total.Calls > 0 {
				total.AvgLatencyMS = total.LatencyMS / int64(total.Calls)
			}
			agentStats[name] = total
		}
	}
	toolStats := map[string]evalToolUsage{}
	for _, report := range reports {
		for name, tool := range report.ToolUsage {
			total := toolStats[name]
			total.Calls += tool.Calls
			total.Errors += tool.Errors
			total.LatencyMS += tool.LatencyMS
			if total.Calls > 0 {
				total.AvgLatencyMS = total.LatencyMS / int64(total.Calls)
			}
			toolStats[name] = total
		}
	}
	write(w, http.StatusOK, evalReport{GeneratedAt: time.Now().UTC(), Summary: summary, Categories: categories, AgentStats: agentStats, ToolStats: toolStats, Runs: reports})
}

func (s *Server) buildEvalRunReport(run domain.EvaluationRun, benchmark domain.BenchmarkCase) evalRunReport {
	llmUsages, _ := s.store.LLMUsages(run.TaskID)
	steps, _ := s.store.Steps(run.TaskID)
	toolCalls, _ := s.store.ToolCalls(run.TaskID)
	artifacts, _ := s.store.Artifacts(run.TaskID)
	tokenUsage := evalTokenUsage{}
	agents := map[string]evalAgentUsage{}
	toolUsage := map[string]evalToolUsage{}
	for _, usage := range llmUsages {
		agent := agents[usage.AgentName]
		agent.Calls++
		agent.PromptTokens += usage.PromptTokens
		agent.CompletionTokens += usage.CompletionTokens
		agent.TotalTokens += usage.TotalTokens
		agent.EstimatedCostUSD += usage.EstimatedCostUSD
		agent.LatencyMS += usage.LatencyMS
		agents[usage.AgentName] = agent
		tokenUsage.PromptTokens += usage.PromptTokens
		tokenUsage.CompletionTokens += usage.CompletionTokens
		tokenUsage.TotalTokens += usage.TotalTokens
		tokenUsage.EstimatedCostUSD += usage.EstimatedCostUSD
	}
	stepCounts := map[string]int{}
	stepByID := map[string]string{}
	for _, step := range steps {
		stepCounts[step.AgentName]++
		stepByID[step.ID] = step.AgentName
	}
	for name, count := range stepCounts {
		agent := agents[name]
		agent.Steps = count
		if agent.Calls > 0 {
			agent.AvgLatencyMS = agent.LatencyMS / int64(agent.Calls)
		}
		agents[name] = agent
	}
	for _, call := range toolCalls {
		tool := toolUsage[call.ToolName]
		tool.Calls++
		tool.LatencyMS += call.LatencyMS
		if call.Status == "FAILED" {
			tool.Errors++
		}
		if tool.Calls > 0 {
			tool.AvgLatencyMS = tool.LatencyMS / int64(tool.Calls)
		}
		toolUsage[call.ToolName] = tool
		if agentName := stepByID[call.StepID]; agentName != "" {
			agent := agents[agentName]
			agent.ToolCalls++
			if call.Status == "FAILED" {
				agent.ToolErrors++
			}
			agents[agentName] = agent
		}
	}
	artifactStats := evalArtifactStats{Count: len(artifacts)}
	changedFiles := []string{}
	for _, artifact := range artifacts {
		switch artifact.Type {
		case "patch_proposal":
			artifactStats.PatchBytes += len(artifact.Content)
			artifactStats.PatchText += artifact.Content
		case "explanation":
			artifactStats.ExplanationChars += len(artifact.Content)
			artifactStats.ExplanationText += artifact.Content
		case "test_report":
			var report struct {
				ChangedFiles []string `json:"changed_files"`
			}
			if json.Unmarshal([]byte(artifact.Content), &report) == nil {
				changedFiles = appendUnique(changedFiles, report.ChangedFiles...)
			}
		}
	}
	externalError := evalExternalError(run.Notes)
	score, breakdown := scoreEvalRun(run, benchmark, evalCategory(benchmark.Name), tokenUsage.TotalTokens, run.RepairAttempts, artifactStats, changedFiles, externalError)
	category := evalCategory(benchmark.Name)
	dimensions := evalRunDimensions(run, benchmark, category, tokenUsage, run.DurationMS, run.RepairAttempts, artifactStats, changedFiles, agents, toolUsage, externalError)
	trace := buildEvalTrace(steps, toolCalls, llmUsages, artifacts)
	return evalRunReport{
		RunID:          run.ID,
		TaskID:         run.TaskID,
		CaseID:         run.CaseID,
		CaseName:       benchmark.Name,
		Category:       evalCategory(benchmark.Name),
		Mode:           run.Mode,
		Status:         run.Status,
		Passed:         run.Passed,
		CreatedAt:      run.CreatedAt,
		DurationMS:     run.DurationMS,
		QualityScore:   score,
		ScoreBreakdown: breakdown,
		TokenUsage:     tokenUsage,
		Agents:         agents,
		ToolUsage:      toolUsage,
		Dimensions:     dimensions,
		Trace:          trace,
		RepairAttempts: run.RepairAttempts,
		MemoryHits:     run.MemoryHits,
		Artifacts:      artifactStats,
		ChangedFiles:   changedFiles,
		ExpectedPaths:  benchmark.Expected,
		ExternalError:  externalError,
	}
}

func scoreEvalRun(run domain.EvaluationRun, benchmark domain.BenchmarkCase, category string, totalTokens int, repairAttempts int, artifacts evalArtifactStats, changedFiles []string, externalError bool) (float64, map[string]float64) {
	breakdown := map[string]float64{}
	if externalError {
		breakdown["external_error"] = 100
		return 0, breakdown
	}
	completion := 0.0
	if run.Status == "completed" && run.Passed {
		completion = 20
	}
	breakdown["completion"] = completion

	deliverable := 0.0
	deliverableMax := 60.0
	switch category {
	case "explanation":
		expectedMentions := 0
		explanationText := strings.ToLower(artifacts.ExplanationText)
		for _, path := range benchmark.Expected {
			if strings.Contains(explanationText, strings.ToLower(path)) {
				expectedMentions++
			}
		}
		if artifacts.ExplanationChars > 0 {
			deliverable += 35
		}
		if expectedMentions == len(benchmark.Expected) && len(benchmark.Expected) > 0 {
			deliverable += 25
		} else if expectedMentions > 0 {
			deliverable += 10
		}
	case "documentation":
		if run.Passed {
			if matchesExpectedPath(changedFiles, benchmark.Expected) {
				deliverable += 20
			}
			if artifacts.PatchBytes > 0 {
				deliverable += 15
			}
			if artifacts.PatchBytes >= 500 {
				deliverable += 10
			}
			deliverable += 15
		}
	default:
		if artifacts.PatchBytes > 0 {
			deliverable += 15
		}
		if run.Passed {
			deliverable += 15
			if matchesExpectedPath(changedFiles, benchmark.Expected) {
				deliverable += 30
			}
		}
	}
	deliverable = minFloat(deliverable, deliverableMax)
	breakdown["deliverable"] = deliverable

	repairScore := 5.0
	if !run.Passed {
		repairScore = 0
	} else if repairAttempts > 1 {
		repairScore = maxFloat(0, 5.0-float64(repairAttempts-1))
	}
	breakdown["repair_efficiency"] = repairScore

	tokenScore := 5.0
	if !run.Passed {
		tokenScore = 0
	} else if totalTokens > 0 {
		idealTokens := idealTokensForCategory(category)
		tokenScore = 5.0 * minFloat(1, float64(idealTokens)/float64(totalTokens))
	}
	breakdown["token_efficiency"] = tokenScore

	total := completion + deliverable + repairScore + tokenScore
	return total, breakdown
}

func evalRunDimensions(run domain.EvaluationRun, benchmark domain.BenchmarkCase, category string, tokenUsage evalTokenUsage, durationMS int64, repairAttempts int, artifacts evalArtifactStats, changedFiles []string, agents map[string]evalAgentUsage, toolUsage map[string]evalToolUsage, externalError bool) map[string]evalDimension {
	if externalError {
		return map[string]evalDimension{
			"external_error": {Score: 0, Max: 100, Label: "External error", Details: map[string]any{"error": run.Notes}},
		}
	}
	expectedHit := matchesExpectedPath(changedFiles, benchmark.Expected)
	resultScore := 0.0
	if run.Passed {
		resultScore = 100
		if len(benchmark.Expected) > 0 && !expectedHit {
			resultScore -= 20
		}
		if category != "explanation" && artifacts.PatchBytes == 0 {
			resultScore -= 20
		}
	} else if artifacts.PatchBytes > 0 {
		resultScore = 20
	}
	resultScore = clampDimension(resultScore)

	planningScore := 100.0
	planner := agents["planner"]
	if planner.Calls == 0 {
		planningScore -= 40
	}
	if repairAttempts > 1 {
		planningScore -= float64(repairAttempts-1) * 10
	}
	if planner.TotalTokens > 30000 {
		planningScore -= 10
	}
	planningScore = clampDimension(planningScore)

	idealTokens := idealTokensForCategory(category)
	tokenRatio := 1.0
	if tokenUsage.TotalTokens > 0 {
		tokenRatio = minFloat(1, float64(idealTokens)/float64(tokenUsage.TotalTokens))
	}
	idealDurationMS := int64(300000)
	if category == "explanation" || category == "documentation" {
		idealDurationMS = 120000
	}
	durationRatio := 1.0
	if durationMS > 0 {
		durationRatio = minFloat(1, float64(idealDurationMS)/float64(durationMS))
	}
	totalToolCalls := 0
	totalToolErrors := 0
	for _, tool := range toolUsage {
		totalToolCalls += tool.Calls
		totalToolErrors += tool.Errors
	}
	toolRatio := 1.0
	if totalToolCalls > 0 {
		toolRatio = minFloat(1, 20/float64(totalToolCalls))
	}
	efficiencyScore := 100 * (0.4*tokenRatio + 0.3*durationRatio + 0.3*toolRatio)
	if !run.Passed {
		efficiencyScore *= 0.6
	}
	efficiencyScore = clampDimension(efficiencyScore)

	safetyScore := 100.0
	if totalToolErrors > 0 {
		safetyScore -= float64(totalToolErrors) * 10
	}
	if len(benchmark.Expected) > 0 && !expectedHit && len(changedFiles) > 0 {
		safetyScore -= 20
	}
	for _, file := range changedFiles {
		if sensitiveEvalPath(file) {
			safetyScore = 0
			break
		}
	}
	if len(changedFiles) > 4 {
		safetyScore -= 10
	}
	safetyScore = clampDimension(safetyScore)

	return map[string]evalDimension{
		"result_usability": {Score: resultScore, Max: 100, Label: "Result usability", Details: map[string]any{
			"passed": run.Passed, "expected_path_hit": expectedHit, "patch_bytes": artifacts.PatchBytes, "explanation_chars": artifacts.ExplanationChars, "changed_files": changedFiles,
		}},
		"planning": {Score: planningScore, Max: 100, Label: "Planning quality", Details: map[string]any{
			"planner_calls": planner.Calls, "planner_tokens": planner.TotalTokens, "repair_attempts": repairAttempts,
		}},
		"efficiency": {Score: efficiencyScore, Max: 100, Label: "Efficiency", Details: map[string]any{
			"total_tokens": tokenUsage.TotalTokens, "estimated_cost_usd": tokenUsage.EstimatedCostUSD, "duration_ms": durationMS, "tool_calls": totalToolCalls, "token_ratio": tokenRatio, "duration_ratio": durationRatio,
		}},
		"safety": {Score: safetyScore, Max: 100, Label: "Safety", Details: map[string]any{
			"tool_errors": totalToolErrors, "changed_file_count": len(changedFiles), "changed_files": changedFiles, "expected_paths": benchmark.Expected,
		}},
	}
}

func clampDimension(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func sensitiveEvalPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(base))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	switch ext {
	case ".pem", ".key", ".p12", ".pfx", ".jks":
		return true
	}
	return strings.Contains(base, "credential") || strings.Contains(base, "secret")
}

func buildEvalTrace(steps []domain.TaskStep, toolCalls []domain.ToolCall, llmUsages []domain.LLMUsage, artifacts []domain.Artifact) evalTraceAnalysis {
	events := []evalTraceEvent{}
	phases := map[string]evalPhaseStats{}
	stepByID := map[string]domain.TaskStep{}
	for _, step := range steps {
		stepByID[step.ID] = step
		phase := evalTracePhase(step)
		attempt := evalStepAttempt(step.Input)
		events = append(events, evalTraceEvent{ID: step.ID, Type: "step", Agent: step.AgentName, Phase: phase, Attempt: attempt, Status: step.Status, Label: step.StepType, StartedAt: step.StartedAt, LatencyMS: step.LatencyMS})
		stats := phases[phase]
		stats.Calls++
		stats.LatencyMS += step.LatencyMS
		phases[phase] = stats
	}
	for _, call := range toolCalls {
		phase := "tool"
		agent := ""
		if step, ok := stepByID[call.StepID]; ok {
			phase = evalTracePhase(step)
			agent = step.AgentName
		}
		events = append(events, evalTraceEvent{ID: call.ID, Type: "tool", Agent: agent, Phase: phase, Status: call.Status, Label: call.ToolName, StartedAt: call.StartedAt, LatencyMS: call.LatencyMS})
		stats := phases[phase]
		stats.ToolCalls++
		if call.Status == "FAILED" {
			stats.ToolErrors++
		}
		phases[phase] = stats
	}
	for _, usage := range llmUsages {
		phase := "llm"
		if step, ok := stepByID[usage.StepID]; ok {
			phase = evalTracePhase(step)
		}
		events = append(events, evalTraceEvent{ID: usage.ID, Type: "llm", Agent: usage.AgentName, Phase: phase, Label: usage.Model, StartedAt: usage.CreatedAt, LatencyMS: usage.LatencyMS, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens, EstimatedCostUSD: usage.EstimatedCostUSD})
		stats := phases[phase]
		stats.LLMCalls++
		stats.Tokens += usage.TotalTokens
		phases[phase] = stats
	}
	for _, artifact := range artifacts {
		summary := traceArtifactSummary(artifact)
		if summary == "" {
			continue
		}
		events = append(events, evalTraceEvent{ID: artifact.ID, Type: "artifact", Phase: "artifact", Label: artifact.Type, StartedAt: artifact.CreatedAt, Summary: summary})
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].StartedAt.Equal(events[j].StartedAt) {
			return events[i].ID < events[j].ID
		}
		return events[i].StartedAt.Before(events[j].StartedAt)
	})
	return evalTraceAnalysis{Phases: phases, Events: events}
}

func evalTracePhase(step domain.TaskStep) string {
	switch step.StepType {
	case "PLANNING", "REPLAN_REQUIRED":
		return "planning"
	case "RETRIEVING_CONTEXT":
		return "retrieval"
	case "GENERATING_PATCH":
		return "patch"
	case "RUNNING_TESTS":
		return "validation"
	case "REVIEWING":
		return "review"
	case "EXPLAINING":
		return "explanation"
	default:
		return strings.ToLower(strings.ReplaceAll(step.StepType, "_", "-"))
	}
}

func evalStepAttempt(input any) int {
	payload, ok := input.(map[string]any)
	if !ok {
		return 0
	}
	switch value := payload["attempt"].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		parsed, _ := strconv.Atoi(value)
		return parsed
	}
	return 0
}

func traceArtifactSummary(artifact domain.Artifact) string {
	switch artifact.Type {
	case "test_report":
		var report struct {
			Status  string `json:"status"`
			Applied bool   `json:"applied"`
			Passed  bool   `json:"passed"`
		}
		if json.Unmarshal([]byte(artifact.Content), &report) == nil {
			return report.Status + " applied=" + strconv.FormatBool(report.Applied) + " passed=" + strconv.FormatBool(report.Passed)
		}
	case "review":
		for _, line := range strings.Split(strings.TrimSpace(artifact.Content), "\n") {
			if strings.TrimSpace(line) != "" {
				return strings.TrimSpace(line)
			}
		}
	case "patch_proposal":
		return strconv.Itoa(len(artifact.Content)) + " bytes"
	}
	return ""
}

func idealTokensForCategory(category string) int {
	switch category {
	case "explanation":
		return 4000
	case "documentation":
		return 6000
	case "security", "refactor":
		return 12000
	default:
		return 10000
	}
}

func matchesExpectedPath(changedFiles, expected []string) bool {
	for _, file := range changedFiles {
		lowerFile := strings.ToLower(file)
		for _, path := range expected {
			if strings.Contains(lowerFile, strings.ToLower(path)) {
				return true
			}
		}
	}
	return false
}

func appendUnique(target []string, values ...string) []string {
	seen := map[string]bool{}
	for _, value := range target {
		seen[value] = true
	}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			target = append(target, value)
		}
	}
	return target
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func maxFloat(left, right float64) float64 {
	if right > left {
		return right
	}
	return left
}

func minFloat(left, right float64) float64 {
	if right < left {
		return right
	}
	return left
}
