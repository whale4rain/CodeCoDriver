package skills

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

//go:embed defaults.json
var defaultsJSON embed.FS

type Registry struct {
	mu     sync.RWMutex
	skills map[string]Skill
	order  []string
}

func New() *Registry {
	return &Registry{skills: map[string]Skill{}}
}

func DefaultRegistry() *Registry {
	registry := New()
	data, err := defaultsJSON.ReadFile("defaults.json")
	if err != nil {
		_ = registry.Register(Skill{Name: "general", Description: "Default engineering task", Workflow: "standard_agent_loop"})
		return registry
	}
	_ = registry.Load(data)
	return registry
}

func (r *Registry) Register(skill Skill) error {
	if err := skill.Validate(); err != nil {
		return err
	}
	skill.Name = strings.TrimSpace(skill.Name)
	if skill.Workflow == "" {
		skill.Workflow = "standard_agent_loop"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.skills[skill.Name]; !exists {
		r.order = append(r.order, skill.Name)
	}
	r.skills[skill.Name] = skill
	return nil
}

func (r *Registry) Get(name string) (Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	skill, ok := r.skills[strings.TrimSpace(name)]
	return skill, ok
}

func (r *Registry) List() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Skill, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.skills[name])
	}
	return out
}

func (r *Registry) Load(data []byte) error {
	var items []Skill
	if err := json.Unmarshal(data, &items); err != nil {
		var single Skill
		if singleErr := json.Unmarshal(data, &single); singleErr != nil {
			return fmt.Errorf("decode skills JSON: %w", err)
		}
		items = []Skill{single}
	}
	for _, skill := range items {
		if err := r.Register(skill); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return r.Load(data)
}

func (r *Registry) SortedNames() []string {
	skills := r.List()
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	sort.Strings(names)
	return names
}
