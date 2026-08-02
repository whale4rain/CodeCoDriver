package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type Result struct {
	Content  any            `json:"content"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type Tool interface {
	Name() string
	Call(context.Context, map[string]any) (Result, error)
}

type Gateway struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewGateway() *Gateway { return &Gateway{tools: map[string]Tool{}} }

func (g *Gateway) Register(tool Tool) error {
	if tool == nil || tool.Name() == "" {
		return fmt.Errorf("tool name is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.tools[tool.Name()]; exists {
		return fmt.Errorf("tool already registered: %s", tool.Name())
	}
	g.tools[tool.Name()] = tool
	return nil
}

func (g *Gateway) Call(ctx context.Context, name string, arguments map[string]any) (Result, error) {
	g.mu.RLock()
	tool, ok := g.tools[name]
	g.mu.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("tool not found: %s", name)
	}
	return tool.Call(ctx, arguments)
}

func (g *Gateway) Names() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	names := make([]string, 0, len(g.tools))
	for name := range g.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type LocalTool struct {
	ToolName string
	Handler  func(context.Context, map[string]any) (Result, error)
}

func (t LocalTool) Name() string { return t.ToolName }
func (t LocalTool) Call(ctx context.Context, arguments map[string]any) (Result, error) {
	if t.Handler == nil {
		return Result{}, fmt.Errorf("tool handler is nil: %s", t.ToolName)
	}
	return t.Handler(ctx, arguments)
}
