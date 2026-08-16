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
		vars := skillPromptVariables(r, skill, prompt)
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

func skillPromptVariables(r AgentRequest, skill skills.Skill, template skills.PromptTemplate) map[string]string {
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
	if promptUsesVariable(template, "context_json") {
		if contextJSON, ok := r.Context["context_json"].(string); ok {
			vars["context_json"] = contextJSON
		} else if data, err := json.Marshal(r.Context); err == nil {
			vars["context_json"] = string(data)
		}
	}
	return vars
}

func promptUsesVariable(template skills.PromptTemplate, name string) bool {
	marker := "{{" + name + "}}"
	return strings.Contains(template.User, marker) || strings.Contains(template.System, marker)
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

func humanFeedbackContext(r AgentRequest) string {
	parts := []string{}
	if feedback, ok := r.Context["human_feedback"].(string); ok && strings.TrimSpace(feedback) != "" {
		parts = append(parts, "HUMAN FEEDBACK: "+strings.TrimSpace(feedback))
	}
	if review, ok := r.Context["previous_review"].(string); ok && strings.TrimSpace(review) != "" {
		parts = append(parts, "PREVIOUS REVIEW: "+truncateFeedback(review))
	}
	if patch, ok := r.Context["previous_patch"].(string); ok && strings.TrimSpace(patch) != "" {
		parts = append(parts, "PREVIOUS PATCH:\n"+truncateFeedback(patch))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(parts, "\n\n")
}

func leanContextJSON(context map[string]any) (string, error) {
	cloned := make(map[string]any, len(context))
	for key, value := range context {
		cloned[key] = value
	}
	delete(cloned, "memory_candidates")
	if memories, ok := cloned["memory"].([]domain.MemoryEntry); ok {
		cloned["memory_guidance"] = memoryGuidance(memories)
		delete(cloned, "memory")
	}
	if codebase, ok := cloned["codebase"].(map[string]any); ok {
		trimmed := make(map[string]any, len(codebase))
		for key, value := range codebase {
			if key == "context_pack" || key == "context_pack_text" {
				continue
			}
			trimmed[key] = value
		}
		cloned["codebase"] = trimmed
	}
	data, err := json.Marshal(cloned)
	if err != nil {
		return "", err
	}
	cfg := compactConfigFromEnv()
	if approximateTokenCount(string(data)) > cfg.thresholdTokens {
		compactHeavyContext(cloned, cfg)
		data, err = json.Marshal(cloned)
		if err != nil {
			return "", err
		}
	}
	return string(data), nil
}

func contextPackTextFromContext(context map[string]any) string {
	codebase, ok := context["codebase"].(map[string]any)
	if !ok {
		return ""
	}
	text, _ := codebase["context_pack_text"].(string)
	return strings.TrimSpace(text)
}
