package runtime

import (
	"encoding/json"
	"strconv"
	"strings"

	"codecodriver/internal/domain"
	"codecodriver/internal/skills"
)

func applySkillPrompt(r AgentRequest, agent, basePrompt, baseSystem string) (string, string, bool, error) {
	raw, _ := r.Context["skills"].([]skills.Skill)
	for _, skill := range raw {
		prompt, ok := skill.Prompt(agent)
		if !ok || (strings.TrimSpace(prompt.User) == "" && strings.TrimSpace(prompt.System) == "") {
			continue
		}
		vars := skillPromptVariables(r, skill)
		rendered, err := prompt.Render(vars)
		if err != nil {
			return "", "", false, err
		}
		if strings.TrimSpace(rendered) != "" {
			basePrompt += "\n\nSKILL [" + skill.Name + "] RULES:\n" + rendered
		}
		system, err := prompt.RenderSystem(vars)
		if err != nil {
			return "", "", false, err
		}
		if strings.TrimSpace(system) != "" {
			baseSystem = system
		}
		return basePrompt, baseSystem, true, nil
	}
	return basePrompt, baseSystem, false, nil
}

func skillPromptVariables(r AgentRequest, skill skills.Skill) map[string]string {
	memories, _ := r.Context["memory"].([]domain.MemoryEntry)
	vars := map[string]string{
		"task_title":        r.Task.Title,
		"task_description":  r.Task.Description,
		"repository_name":   r.Repository.Name,
		"primary_language":  r.Repository.PrimaryLanguage,
		"indexed_files":     strconv.Itoa(len(r.Files)),
		"indexed_symbols":   strconv.Itoa(len(r.Symbols)),
		"attempt":           strconv.Itoa(r.Attempt),
		"selected_skill":    skill.Name,
		"selected_workflow": skill.Workflow,
		"memory_guidance":   memoryGuidance(memories),
		"repair_feedback":   "{}",
		"previous_patch":    "",
		"context_json":      "{}",
	}
	if feedback, ok := r.Context["repair_feedback"]; ok {
		vars["repair_feedback"] = marshalArtifact(feedback)
	}
	if patch, ok := r.Context["patch"].(map[string]any); ok {
		if proposal, ok := patch["proposal"].(string); ok {
			vars["previous_patch"] = proposal
		}
	}
	if contextJSON, ok := r.Context["context_json"].(string); ok {
		vars["context_json"] = contextJSON
	} else if data, err := json.Marshal(r.Context); err == nil {
		vars["context_json"] = string(data)
	}
	return vars
}

func skillPathFiles(r AgentRequest) []string {
	raw, _ := r.Context["skills"].([]skills.Skill)
	out := []string{}
	for _, file := range r.Files {
		for _, skill := range raw {
			if skill.MatchesPath(file.Path) {
				out = appendUniquePath(out, file.Path)
				break
			}
		}
	}
	return out
}
