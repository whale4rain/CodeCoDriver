package runtime

import (
	"context"
	"fmt"
	"strings"

	"codecodriver/internal/llm"
)

type ExplainAgent struct{ LLM llm.Client }

func (ExplainAgent) Name() string { return "explainer" }

func (a ExplainAgent) Run(ctx context.Context, r AgentRequest) (AgentResult, error) {
	prompt := fmt.Sprintf("Repository: %s\nTask: %s\n\nExplain the requested code behavior, implementation path, architecture, file, function, or abstraction using only the retrieved context. Do not propose production patches unless the user explicitly asks for a change.", r.Repository.Name, r.Task.Description)
	if pack := explanationContextPack(r); pack != "" {
		prompt += "\n\nRETRIEVED CONTEXT:\n" + truncateFeedback(pack)
	}
	prompt, system, _, err := applySkillPrompt(r, "explainer", prompt, "You are the Code Explainer Agent in CodeCoDriver. Be precise, structured, and evidence-based.")
	if err != nil {
		return AgentResult{}, err
	}
	if a.LLM != nil {
		content, err := a.LLM.Complete(ctx, system, prompt)
		if err != nil {
			return AgentResult{}, err
		}
		return AgentResult{Output: map[string]any{"provider": "deepseek", "model": llm.DefaultDeepSeekModel, "explanation": content}, ArtifactType: "explanation", ArtifactName: "explanation.md", ArtifactContent: content}, nil
	}
	content := "Read-only explanation task. Connect a DeepSeek client to generate the explanation artifact."
	return AgentResult{Output: map[string]any{"explanation": content}, ArtifactType: "explanation", ArtifactName: "explanation.md", ArtifactContent: content}, nil
}

func explanationContextPack(r AgentRequest) string {
	codebase, ok := r.Context["codebase"].(map[string]any)
	if !ok {
		return ""
	}
	if text, ok := codebase["context_pack_text"].(string); ok {
		return strings.TrimSpace(text)
	}
	pack, ok := codebase["context_pack"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(pack)
}
