package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"codecodriver/internal/domain"
)

type evalReport struct {
	GeneratedAt time.Time                    `json:"generated_at"`
	Summary     evalReportSummary            `json:"summary"`
	Categories  map[string]evalCategoryStats `json:"categories"`
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
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
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
	write(w, http.StatusOK, evalReport{GeneratedAt: time.Now().UTC(), Summary: summary, Categories: categories, Runs: reports})
}

func (s *Server) buildEvalRunReport(run domain.EvaluationRun, benchmark domain.BenchmarkCase) evalRunReport {
	llmUsages, _ := s.store.LLMUsages(run.TaskID)
	steps, _ := s.store.Steps(run.TaskID)
	artifacts, _ := s.store.Artifacts(run.TaskID)
	tokenUsage := evalTokenUsage{}
	agents := map[string]evalAgentUsage{}
	for _, usage := range llmUsages {
		agent := agents[usage.AgentName]
		agent.Calls++
		agent.PromptTokens += usage.PromptTokens
		agent.CompletionTokens += usage.CompletionTokens
		agent.TotalTokens += usage.TotalTokens
		agent.EstimatedCostUSD += usage.EstimatedCostUSD
		agents[usage.AgentName] = agent
		tokenUsage.PromptTokens += usage.PromptTokens
		tokenUsage.CompletionTokens += usage.CompletionTokens
		tokenUsage.TotalTokens += usage.TotalTokens
		tokenUsage.EstimatedCostUSD += usage.EstimatedCostUSD
	}
	stepCounts := map[string]int{}
	for _, step := range steps {
		stepCounts[step.AgentName]++
	}
	for name, agent := range agents {
		agent.Steps = stepCounts[name]
		agents[name] = agent
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
