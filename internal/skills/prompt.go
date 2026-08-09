package skills

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type PromptTemplate struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	System      string `json:"system,omitempty"`
	User        string `json:"user,omitempty"`
}

var promptPlaceholder = regexp.MustCompile(`\{\{([A-Za-z0-9_.]+)\}\}`)

func (p PromptTemplate) Render(vars map[string]string) (string, error) {
	if strings.TrimSpace(p.User) == "" {
		return "", nil
	}
	return renderTemplate(p.User, vars)
}

func (p PromptTemplate) RenderSystem(vars map[string]string) (string, error) {
	if strings.TrimSpace(p.System) == "" {
		return "", nil
	}
	return renderTemplate(p.System, vars)
}

func renderTemplate(template string, vars map[string]string) (string, error) {
	missing := map[string]bool{}
	rendered := promptPlaceholder.ReplaceAllStringFunc(template, func(match string) string {
		key := strings.Trim(match, "{}")
		value, ok := vars[key]
		if !ok {
			missing[key] = true
			return match
		}
		return value
	})
	if len(missing) > 0 {
		keys := make([]string, 0, len(missing))
		for key := range missing {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return "", fmt.Errorf("missing prompt variables: %s", strings.Join(keys, ", "))
	}
	return rendered, nil
}
