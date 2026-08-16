package runtime

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"codecodriver/internal/sandbox"
)

const (
	defaultCompactThresholdTokens = 60000
	defaultCompactKeepRecentTurns = 2
	defaultCompactMaxToolBytes    = 8 * 1024
	defaultCompactMaxContextBytes = 16 * 1024
	compactMaxHistorySummaryTurns = 20
)

type compactConfig struct {
	thresholdTokens int
	keepRecentTurns int
	maxToolBytes    int
	maxContextBytes int
}

var toolTranscriptBoundary = regexp.MustCompile(`\n\n(TOOL_RESULT(?:_ERROR)?\([^\n]*\):)`)

func compactConfigFromEnv() compactConfig {
	return compactConfig{
		thresholdTokens: envInt("CODECODRIVER_CONTEXT_COMPACT_TOKENS", defaultCompactThresholdTokens),
		keepRecentTurns: envInt("CODECODRIVER_CONTEXT_KEEP_TURNS", defaultCompactKeepRecentTurns),
		maxToolBytes:    envInt("CODECODRIVER_TOOL_RESULT_MAX_BYTES", defaultCompactMaxToolBytes),
		maxContextBytes: envInt("CODECODRIVER_CONTEXT_VALUE_MAX_BYTES", defaultCompactMaxContextBytes),
	}
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func approximateTokenCount(value string) int {
	if value == "" {
		return 0
	}
	// This is a conservative preflight estimate for mixed code and prose. The
	// provider reports exact token counts after the request returns.
	return (len(value) + 3) / 4
}

func compactString(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	half := maxBytes / 2
	if half <= 0 {
		half = 1
	}
	head := safePrefixBytes(value, half)
	tail := safeSuffixBytes(value, half)
	return head + fmt.Sprintf("\n...[compacted %d bytes]...\n", len(value)-maxBytes) + tail
}

func safePrefixBytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit >= len(value) {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func safeSuffixBytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit >= len(value) {
		return value
	}
	cut := len(value) - limit
	for cut < len(value) && !utf8.RuneStart(value[cut]) {
		cut++
	}
	return value[cut:]
}

func compactAttemptHistory(history []map[string]any, cfg compactConfig) []map[string]any {
	if len(history) <= cfg.keepRecentTurns {
		return history
	}
	recent := make([]map[string]any, 0, cfg.keepRecentTurns)
	recent = append(recent, history[len(history)-cfg.keepRecentTurns:]...)
	older := history[:len(history)-cfg.keepRecentTurns]

	var summary strings.Builder
	summary.WriteString("Earlier attempts:\n")
	limit := len(older)
	if limit > compactMaxHistorySummaryTurns {
		limit = compactMaxHistorySummaryTurns
	}
	for _, item := range older[len(older)-limit:] {
		attempt, _ := item["attempt"]
		status, _ := item["status"]
		errorKind, _ := item["error_kind"]
		changed, _ := item["changed_files"]
		summary.WriteString(fmt.Sprintf("- attempt=%v status=%v error=%v files=%v\n", attempt, status, errorKind, changed))
	}
	if len(older) > limit {
		summary.WriteString(fmt.Sprintf("... %d earlier attempts omitted\n", len(older)-limit))
	}
	summaryEntry := map[string]any{
		"attempt":        0,
		"status":         "compacted_history",
		"error_kind":     "compacted_history",
		"changed_files":  []string{},
		"compacted_text": summary.String(),
	}
	return append([]map[string]any{summaryEntry}, recent...)
}

func compactHeavyContext(context map[string]any, cfg compactConfig) {
	for _, key := range []string{"previous_patch", "previous_review", "previous_test_report", "initial_plan"} {
		if value, ok := context[key].(string); ok {
			context[key] = compactString(value, cfg.maxContextBytes)
		}
	}
	if report, ok := context["test"].(sandbox.Report); ok {
		report.Output = compactString(report.Output, cfg.maxContextBytes)
		report.Error = compactString(report.Error, cfg.maxContextBytes)
		context["test"] = report
	}
	for _, key := range []string{"patch", "reviewer", "repair_feedback", "planner", "initial_plan"} {
		raw, ok := context[key].(map[string]any)
		if !ok {
			continue
		}
		copy := make(map[string]any, len(raw))
		for name, value := range raw {
			if text, ok := value.(string); ok {
				copy[name] = compactString(text, cfg.maxContextBytes)
			} else {
				copy[name] = value
			}
		}
		context[key] = copy
	}
	if history, ok := context["attempt_history"].([]map[string]any); ok {
		context["attempt_history"] = compactAttemptHistory(history, cfg)
	}
}

func compactToolTranscript(transcript string, cfg compactConfig) string {
	if transcript == "" {
		return transcript
	}
	entries := splitToolTranscript(transcript)
	if len(entries) <= cfg.keepRecentTurns {
		return compactRecentToolEntries(transcript, cfg)
	}

	older := entries[:len(entries)-cfg.keepRecentTurns]
	recent := entries[len(entries)-cfg.keepRecentTurns:]
	var summary strings.Builder
	summary.WriteString("\n\nCOMPACTED_TOOL_HISTORY:\n")
	for _, entry := range older {
		name := toolNameFromEntry(entry)
		size := approximateTokenCount(entry)
		summary.WriteString(fmt.Sprintf("- %s: %d estimated tokens\n", name, size))
	}
	summary.WriteString("\nRECENT TOOL RESULTS:\n")
	for _, entry := range recent {
		summary.WriteString(compactString(entry, cfg.maxToolBytes))
	}
	return summary.String()
}

func compactRecentToolEntries(transcript string, cfg compactConfig) string {
	entries := splitToolTranscript(transcript)
	if len(entries) == 0 {
		return compactString(transcript, cfg.maxToolBytes)
	}
	var out strings.Builder
	for _, entry := range entries {
		out.WriteString(compactString(entry, cfg.maxToolBytes))
	}
	return out.String()
}

func splitToolTranscript(transcript string) []string {
	indexes := toolTranscriptBoundary.FindAllStringIndex(transcript, -1)
	if len(indexes) == 0 {
		return []string{transcript}
	}
	out := make([]string, 0, len(indexes))
	for i, span := range indexes {
		start := span[0] + len("\n\n")
		end := len(transcript)
		if i+1 < len(indexes) {
			end = indexes[i+1][0] + len("\n\n")
		}
		out = append(out, transcript[start:end])
	}
	return out
}

func toolNameFromEntry(entry string) string {
	if start := strings.Index(entry, "TOOL_RESULT("); start >= 0 {
		body := entry[start+len("TOOL_RESULT("):]
		if end := strings.Index(body, ")"); end >= 0 {
			return strings.TrimSpace(body[:end])
		}
	}
	if start := strings.Index(entry, "TOOL_RESULT_ERROR("); start >= 0 {
		body := entry[start+len("TOOL_RESULT_ERROR("):]
		if end := strings.Index(body, ")"); end >= 0 {
			return strings.TrimSpace(body[:end])
		}
	}
	return "unknown"
}

func maybeCompactAgentPrompt(initialPrompt, transcript string, cfg compactConfig) string {
	full := initialPrompt + transcript
	if approximateTokenCount(full) <= cfg.thresholdTokens {
		return full
	}
	return initialPrompt + compactToolTranscript(transcript, cfg)
}
