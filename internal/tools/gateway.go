package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"codecodriver/internal/sandbox"
)

type Result struct {
	Content  any            `json:"content"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Workspace is the file operation surface exposed to agent tools. The concrete
// implementation is owned by the sandbox package; tools only depend on this
// minimal contract so the host repository is never an implicit fallback.
type Workspace interface {
	ReadFile(context.Context, string, int, int) (map[string]any, error)
	SearchFiles(context.Context, string, int) ([]map[string]any, error)
	ReadSymbols(context.Context, string, int) ([]map[string]any, error)
	EditFile(context.Context, string, string, string, string, int, int) (map[string]any, error)
	WriteFile(context.Context, string, string) (map[string]any, error)
	GeneratePatch(context.Context) (string, error)
	Reset(context.Context) error
	RunTest(context.Context, string) sandbox.Report
	Close(context.Context) error
}

type Policy struct {
	Allowed      map[string]bool
	AgentAllowed map[string]map[string]bool
	Timeout      time.Duration
	Retries      int
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

// ToolSpec is a provider-neutral description used to build native function
// schemas for the LLM. Runtime agents translate these into llm.Tool objects.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type Gateway struct {
	mu       sync.RWMutex
	tools    map[string]Tool
	schemas  map[string]ToolSpec
	policy   Policy
	observer func(AuditRecord)
}

func NewGateway() *Gateway {
	return &Gateway{tools: map[string]Tool{}, schemas: map[string]ToolSpec{}}
}

func (g *Gateway) Register(tool Tool) error {
	return g.RegisterWithSchema(tool, ToolSpec{})
}

func (g *Gateway) RegisterWithSchema(tool Tool, spec ToolSpec) error {
	if tool == nil || tool.Name() == "" {
		return fmt.Errorf("tool name is required")
	}
	if spec.Name == "" {
		spec.Name = tool.Name()
	}
	if spec.Parameters == nil {
		spec.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.tools[tool.Name()]; exists {
		return fmt.Errorf("tool already registered: %s", tool.Name())
	}
	g.tools[tool.Name()] = tool
	g.schemas[tool.Name()] = spec
	return nil
}

func (g *Gateway) SetToolSchema(name string, spec ToolSpec) {
	if spec.Name == "" {
		spec.Name = name
	}
	if spec.Parameters == nil {
		spec.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.schemas[name] = spec
}

func (g *Gateway) ToolSpecs(names []string) []ToolSpec {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]ToolSpec, 0, len(names))
	for _, name := range names {
		if spec, ok := g.schemas[name]; ok {
			out = append(out, spec)
			continue
		}
		out = append(out, ToolSpec{
			Name:       name,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		})
	}
	return out
}

func (g *Gateway) Configure(policy Policy, observer func(AuditRecord)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.policy = policy
	g.observer = observer
}

func (g *Gateway) SetAgentToolPolicy(agent string, allowed ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.policy.AgentAllowed == nil {
		g.policy.AgentAllowed = map[string]map[string]bool{}
	}
	rules := map[string]bool{}
	for _, name := range allowed {
		rules[name] = true
	}
	g.policy.AgentAllowed[agent] = rules
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
	agent := executionValue(ctx, agentKey)
	if len(policy.AgentAllowed) > 0 {
		rules, ok := policy.AgentAllowed[agent]
		if !ok || !rules[name] {
			return Result{}, fmt.Errorf("tool denied for agent %s: %s", agent, name)
		}
	}
	started := time.Now().UTC()
	callCtx := ctx
	var cancel context.CancelFunc
	if policy.Timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, policy.Timeout)
		defer cancel()
	}
	result, err := Result{}, error(nil)
	for attempt := 0; attempt <= policy.Retries; attempt++ {
		result, err = tool.Call(callCtx, arguments)
		if err == nil || callCtx.Err() != nil {
			break
		}
	}
	if observer != nil {
		ended := time.Now().UTC()
		observer(AuditRecord{TaskID: executionValue(ctx, taskKey), RunID: executionValue(ctx, runKey), StepID: executionValue(ctx, stepKey), Name: name, Request: arguments, Result: result, Error: err, StartedAt: started, EndedAt: ended})
	}
	return result, err
}

type executionContextKey string

const (
	taskKey      executionContextKey = "task_id"
	runKey       executionContextKey = "run_id"
	stepKey      executionContextKey = "step_id"
	agentKey     executionContextKey = "agent"
	workspaceKey executionContextKey = "workspace"
)

func WithExecutionContext(ctx context.Context, taskID, runID, stepID string) context.Context {
	ctx = context.WithValue(ctx, taskKey, taskID)
	ctx = context.WithValue(ctx, runKey, runID)
	return context.WithValue(ctx, stepKey, stepID)
}

func WithAgentContext(ctx context.Context, agent string) context.Context {
	return context.WithValue(ctx, agentKey, agent)
}

func WithWorkspaceContext(ctx context.Context, workspace Workspace) context.Context {
	return context.WithValue(ctx, workspaceKey, workspace)
}

func WorkspaceFromContext(ctx context.Context) Workspace {
	workspace, _ := ctx.Value(workspaceKey).(Workspace)
	return workspace
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
