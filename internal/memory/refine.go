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
	if err := validateRefinedMemory(parsed); err != nil {
		return parsed, err
	}
	return parsed, nil
}

func parseRefinedBatch(content string, count int) ([]refinedMemory, error) {
	raw := extractJSONArray(content)
	if raw == "" {
		return nil, fmt.Errorf("refiner returned no JSON array")
	}
	var parsed []refinedMemory
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("parse refiner JSON array: %w", err)
	}
	if len(parsed) != count {
		return nil, fmt.Errorf("refiner returned %d items for %d inputs", len(parsed), count)
	}
	for i := range parsed {
		if err := validateRefinedMemory(parsed[i]); err != nil {
			return nil, fmt.Errorf("validate refiner item %d: %w", i, err)
		}
	}
	return parsed, nil
}

func validateRefinedMemory(parsed refinedMemory) error {
	if strings.TrimSpace(parsed.Summary) == "" && strings.TrimSpace(parsed.RootCause) == "" {
		return fmt.Errorf("refiner JSON missing summary and root_cause")
	}
	return nil
}

func extractJSON(value string) string {
	start := strings.Index(value, "{")
	end := strings.LastIndex(value, "}")
	if start < 0 || end < start {
		return ""
	}
	return value[start : end+1]
}

func extractJSONArray(value string) string {
	start := strings.Index(value, "[")
	end := strings.LastIndex(value, "]")
	if start < 0 || end < start {
		return ""
	}
	return value[start : end+1]
}
