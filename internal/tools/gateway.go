package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Result struct {
	Content  any            `json:"content"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type Policy struct {
	Allowed map[string]bool
	Timeout time.Duration
}

type AuditRecord struct {
	TaskID    string
	RunID     string
	StepID    string
	Name      string
	Request   map[string]any
	Result    Result
	Error     error
	StartedAt time.Time
	EndedAt   time.Time
}

type Tool interface {
	Name() string
	Call(context.Context, map[string]any) (Result, error)
}

type Gateway struct {
	mu       sync.RWMutex
	tools    map[string]Tool
	policy   Policy
	observer func(AuditRecord)
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

func (g *Gateway) Configure(policy Policy, observer func(AuditRecord)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.policy = policy
	g.observer = observer
}

func (g *Gateway) Call(ctx context.Context, name string, arguments map[string]any) (Result, error) {
	g.mu.RLock()
	tool, ok := g.tools[name]
	policy, observer := g.policy, g.observer
	g.mu.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("tool not found: %s", name)
	}
	if len(policy.Allowed) > 0 && !policy.Allowed[name] {
		return Result{}, fmt.Errorf("tool denied by policy: %s", name)
	}
	started := time.Now().UTC()
	callCtx := ctx
	var cancel context.CancelFunc
	if policy.Timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, policy.Timeout)
		defer cancel()
	}
	result, err := tool.Call(callCtx, arguments)
	if observer != nil {
		ended := time.Now().UTC()
		observer(AuditRecord{TaskID: executionValue(ctx, taskKey), RunID: executionValue(ctx, runKey), StepID: executionValue(ctx, stepKey), Name: name, Request: arguments, Result: result, Error: err, StartedAt: started, EndedAt: ended})
	}
	return result, err
}

type executionContextKey string

const (
	taskKey executionContextKey = "task_id"
	runKey  executionContextKey = "run_id"
	stepKey executionContextKey = "step_id"
)

func WithExecutionContext(ctx context.Context, taskID, runID, stepID string) context.Context {
	ctx = context.WithValue(ctx, taskKey, taskID)
	ctx = context.WithValue(ctx, runKey, runID)
	return context.WithValue(ctx, stepKey, stepID)
}

func executionValue(ctx context.Context, key executionContextKey) string {
	value, _ := ctx.Value(key).(string)
	return value
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
