package skills

import (
	"fmt"
	"strings"
)

type Skill struct {
	Name         string                    `json:"name"`
	Description  string                    `json:"description,omitempty"`
	Keywords     []string                  `json:"keywords,omitempty"`
	PathPatterns []string                  `json:"path_patterns,omitempty"`
	Workflow     string                    `json:"workflow,omitempty"`
	Prompts      map[string]PromptTemplate `json:"prompts,omitempty"`
	AllowedTools []string                  `json:"allowed_tools,omitempty"`
	Metadata     map[string]string         `json:"metadata,omitempty"`
}

func (s Skill) Validate() error {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	for agent, prompt := range s.Prompts {
		if strings.TrimSpace(agent) == "" {
			return fmt.Errorf("skill %s has an empty agent name", name)
		}
		if _, err := prompt.Render(map[string]string{
			"task_title":        "title",
			"task_description":  "description",
			"repository_name":   "repo",
			"primary_language":  "go",
			"indexed_files":     "1",
			"indexed_symbols":   "2",
			"attempt":           "1",
			"context_json":      "{}",
			"memory_guidance":   "",
			"repair_feedback":   "{}",
			"previous_patch":    "",
			"selected_skill":    name,
			"selected_workflow": "standard_agent_loop",
		}); err != nil {
			return fmt.Errorf("skill %s prompt for %s: %w", name, agent, err)
		}
		if _, err := prompt.RenderSystem(map[string]string{
			"task_title":        "title",
			"task_description":  "description",
			"repository_name":   "repo",
			"primary_language":  "go",
			"indexed_files":     "1",
			"indexed_symbols":   "2",
			"attempt":           "1",
			"context_json":      "{}",
			"memory_guidance":   "",
			"repair_feedback":   "{}",
			"previous_patch":    "",
			"selected_skill":    name,
			"selected_workflow": "standard_agent_loop",
		}); err != nil {
			return fmt.Errorf("skill %s system prompt for %s: %w", name, agent, err)
		}
	}
	return nil
}

func (s Skill) Prompt(agent string) (PromptTemplate, bool) {
	prompt, ok := s.Prompts[agent]
	return prompt, ok
}

func (s Skill) MatchesPath(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	for _, pattern := range s.PathPatterns {
		if matchPathPattern(strings.ToLower(pattern), normalized) {
			return true
		}
	}
	return false
}

func likelySkill(s Skill) bool {
	return s.Description != "" ||
		len(s.Keywords) > 0 ||
		len(s.PathPatterns) > 0 ||
		(s.Workflow != "" && s.Workflow != "standard_agent_loop") ||
		len(s.Prompts) > 0 ||
		len(s.AllowedTools) > 0 ||
		len(s.Metadata) > 0
}
