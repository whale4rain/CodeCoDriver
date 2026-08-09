package skills

import (
	"fmt"
	"path"
	"strings"
	"unicode"

	"codecodriver/internal/domain"
)

type RouteInput struct {
	Task       domain.Task
	Repository domain.Repository
	Files      []domain.RepositoryFile
	Memories   []domain.MemoryEntry
}

type RouteResult struct {
	PrimarySkill string             `json:"primary_skill"`
	Skills       []Skill            `json:"skills"`
	Workflow     string             `json:"workflow"`
	Reason       string             `json:"reason"`
	Scores       map[string]float64 `json:"scores"`
}

type Router struct {
	registry *Registry
}

func NewRouter(registry *Registry) *Router {
	return &Router{registry: registry}
}

func (r *Router) Route(input RouteInput) (RouteResult, error) {
	registry := r.registry
	if registry == nil {
		registry = DefaultRegistry()
	}
	skills := registry.List()
	if len(skills) == 0 {
		_ = registry.Register(Skill{Name: "general", Workflow: "standard_agent_loop"})
		skills = registry.List()
	}

	explicit := strings.TrimSpace(input.Task.SkillName)
	if explicit != "" {
		skill, ok := registry.Get(explicit)
		if !ok {
			return RouteResult{}, fmt.Errorf("unknown skill %q", explicit)
		}
		workflow := skill.Workflow
		if workflow == "" {
			workflow = "standard_agent_loop"
		}
		return RouteResult{
			PrimarySkill: skill.Name,
			Skills:       []Skill{skill},
			Workflow:     workflow,
			Reason:       "explicitly selected by task",
			Scores:       map[string]float64{skill.Name: 1},
		}, nil
	}

	scores := map[string]float64{}
	best, bestScore := Skill{}, 0.0
	for _, skill := range skills {
		score := routeScore(skill, input)
		scores[skill.Name] = score
		if score > bestScore {
			best, bestScore = skill, score
		}
	}
	if bestScore <= 0 {
		general, ok := registry.Get("general")
		if !ok {
			general = Skill{Name: "general", Workflow: "standard_agent_loop"}
		}
		best, bestScore = general, 0
	}
	workflow := best.Workflow
	if workflow == "" {
		workflow = "standard_agent_loop"
	}
	reason := "fallback general workflow"
	if bestScore > 0 {
		reason = fmt.Sprintf("matched by keywords, paths, or memory with score %.1f", bestScore)
	}
	return RouteResult{
		PrimarySkill: best.Name,
		Skills:       []Skill{best},
		Workflow:     workflow,
		Reason:       reason,
		Scores:       scores,
	}, nil
}

func routeScore(skill Skill, input RouteInput) float64 {
	if skill.Name == "general" {
		return 0
	}
	text := strings.ToLower(input.Task.Title + " " + input.Task.Description)
	score := 0.0
	for _, keyword := range skill.Keywords {
		keyword = strings.ToLower(keyword)
		if strings.Contains(text, keyword) {
			score += 2
			continue
		}
		for _, token := range skillTokens(keyword) {
			for _, candidate := range skillTokens(text) {
				if token == candidate {
					score += 0.5
					break
				}
			}
		}
	}
	matchedPaths := 0
	for _, file := range input.Files {
		if skill.MatchesPath(file.Path) {
			matchedPaths++
		}
	}
	if matchedPaths > 0 {
		score += 1.5
	}
	hasDirectSignal := score > 0
	memoryHits := 0
	for _, memory := range input.Memories {
		memoryText := strings.ToLower(memory.Title + " " + memory.Summary + " " + memory.Content)
		for _, keyword := range skill.Keywords {
			if strings.Contains(memoryText, strings.ToLower(keyword)) {
				memoryHits++
				break
			}
		}
	}
	if hasDirectSignal && memoryHits > 0 {
		score += 0.5
	}
	return score
}

func skillTokens(value string) []string {
	lower := strings.ToLower(value)
	seen := map[string]bool{}
	out := []string{}
	for _, token := range strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if len(token) >= 3 && !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
	}
	runes := []rune(lower)
	for i := 0; i < len(runes)-1; i++ {
		if unicode.Is(unicode.Han, runes[i]) || unicode.Is(unicode.Han, runes[i+1]) {
			pair := string(runes[i : i+2])
			if !seen[pair] {
				seen[pair] = true
				out = append(out, pair)
			}
		}
	}
	return out
}

func matchPathPattern(pattern, filePath string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || filePath == "" {
		return false
	}
	if strings.HasPrefix(pattern, "**/") {
		rest := strings.TrimPrefix(pattern, "**/")
		return strings.HasSuffix(filePath, rest) || strings.Contains(filePath, "/"+rest)
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return filePath == prefix || strings.HasPrefix(filePath, prefix+"/")
	}
	matched, _ := path.Match(pattern, filePath)
	return matched
}
