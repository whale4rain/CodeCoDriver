package runtime

import (
	"strings"
	"testing"

	"codecodriver/internal/sandbox"
)

func TestCompactToolTranscriptKeepsRecentAndDropsOldLargeResults(t *testing.T) {
	cfg := compactConfig{thresholdTokens: 1, keepRecentTurns: 2, maxToolBytes: 256, maxContextBytes: 512}
	transcript := "\n\nTOOL_RESULT(read_file):\n" + strings.Repeat("old-a", 2000) +
		"\n\nTOOL_RESULT(search_files):\nsmall old result" +
		"\n\nTOOL_RESULT(read_symbols):\n" + strings.Repeat("recent-b", 2000) +
		"\n\nTOOL_RESULT(generate_patch):\nfinal diff"

	got := compactToolTranscript(transcript, cfg)
	if !strings.Contains(got, "COMPACTED_TOOL_HISTORY") {
		t.Fatalf("missing compacted history marker: %s", got)
	}
	if !strings.Contains(got, "read_file") || !strings.Contains(got, "search_files") {
		t.Fatalf("older tool names were not summarized: %s", got)
	}
	if strings.Contains(got, "old-a") {
		t.Fatalf("large older tool result was retained: %s", got)
	}
	if !strings.Contains(got, "read_symbols") || !strings.Contains(got, "generate_patch") {
		t.Fatalf("recent tool results were dropped: %s", got)
	}
	if strings.Contains(got, strings.Repeat("recent-b", 200)) {
		t.Fatalf("large recent tool result was not bounded: %s", got)
	}
}

func TestCompactAttemptHistoryKeepsRecentTurns(t *testing.T) {
	history := []map[string]any{
		{"attempt": 1, "status": "apply_failed", "error_kind": "stale_or_invalid_hunk"},
		{"attempt": 2, "status": "tests_failed", "error_kind": "tests_failed"},
		{"attempt": 3, "status": "tests_failed", "error_kind": "tests_failed"},
		{"attempt": 4, "status": "passed", "error_kind": ""},
	}
	got := compactAttemptHistory(history, compactConfig{keepRecentTurns: 2})
	if len(got) != 3 {
		t.Fatalf("compacted history length=%d want=3", len(got))
	}
	if got[0]["status"] != "compacted_history" {
		t.Fatalf("first entry=%+v", got[0])
	}
	if got[1]["attempt"] != 3 || got[2]["attempt"] != 4 {
		t.Fatalf("recent entries=%+v", got[1:])
	}
}

func TestCompactHeavyContextBoundsSandboxOutput(t *testing.T) {
	context := map[string]any{
		"previous_patch": strings.Repeat("patch", 5000),
		"test":           sandbox.Report{Output: strings.Repeat("test-output", 5000), Error: "apply failed"},
		"attempt_history": []map[string]any{
			{"attempt": 1, "status": "apply_failed", "changed_files": []string{"a.go"}},
			{"attempt": 2, "status": "passed", "changed_files": []string{"b.go"}},
		},
	}
	compactHeavyContext(context, compactConfig{keepRecentTurns: 1, maxContextBytes: 256})
	if got := context["previous_patch"].(string); len(got) > 512 {
		t.Fatalf("previous patch length=%d", len(got))
	}
	if report := context["test"].(sandbox.Report); len(report.Output) > 512 {
		t.Fatalf("test output length=%d", len(report.Output))
	}
	if history := context["attempt_history"].([]map[string]any); len(history) != 2 {
		t.Fatalf("history length=%d want=2", len(history))
	}
}

func TestCompactStringDoesNotSplitUTF8(t *testing.T) {
	value := strings.Repeat("中", 1000)
	got := compactString(value, 64)
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("compactString produced invalid UTF-8: %q", got)
	}
	if len(got) > 128 {
		t.Fatalf("compacted length=%d", len(got))
	}
}
