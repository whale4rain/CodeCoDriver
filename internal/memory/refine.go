package memory

import (
	"encoding/json"
	"fmt"
	"strings"
)

type refinedMemory struct {
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	Symptom      string   `json:"symptom"`
	RootCause    string   `json:"root_cause"`
	ChangedFiles []string `json:"changed_files"`
	Symbols      []string `json:"symbols"`
	Condition    string   `json:"condition"`
	SuccessScore float64  `json:"success_score"`
}

func parseRefinedMemory(content string) (refinedMemory, error) {
	var parsed refinedMemory
	raw := extractJSON(content)
	if raw == "" {
		return parsed, fmt.Errorf("refiner returned no JSON object")
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return parsed, fmt.Errorf("parse refiner JSON: %w", err)
	}
	if strings.TrimSpace(parsed.Summary) == "" && strings.TrimSpace(parsed.RootCause) == "" {
		return parsed, fmt.Errorf("refiner JSON missing summary and root_cause")
	}
	return parsed, nil
}

func extractJSON(value string) string {
	start := strings.Index(value, "{")
	end := strings.LastIndex(value, "}")
	if start < 0 || end < start {
		return ""
	}
	return value[start : end+1]
}
