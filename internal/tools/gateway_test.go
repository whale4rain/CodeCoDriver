package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayRoutesLocalTool(t *testing.T) {
	gateway := NewGateway()
	if err := gateway.Register(LocalTool{ToolName: "echo", Handler: func(_ context.Context, args map[string]any) (Result, error) {
		return Result{Content: args["value"]}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := gateway.Call(context.Background(), "echo", map[string]any{"value": "ok"})
	if err != nil || result.Content != "ok" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGatewayPolicyAndAudit(t *testing.T) {
	gateway := NewGateway()
	if err := gateway.Register(LocalTool{ToolName: "echo", Handler: func(_ context.Context, args map[string]any) (Result, error) {
		return Result{Content: args["value"]}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	var audit AuditRecord
	gateway.Configure(Policy{Allowed: map[string]bool{"echo": true}}, func(record AuditRecord) { audit = record })
	ctx := WithExecutionContext(context.Background(), "task-1", "run-1", "step-1")
	if _, err := gateway.Call(ctx, "unknown", nil); err == nil {
		t.Fatal("unknown tool should be rejected")
	}
	if _, err := gateway.Call(ctx, "echo", map[string]any{"value": "ok"}); err != nil {
		t.Fatal(err)
	}
	if audit.TaskID != "task-1" || audit.RunID != "run-1" || audit.StepID != "step-1" || audit.Name != "echo" {
		t.Fatalf("audit=%+v", audit)
	}
}

func TestGatewayRetriesTransientFailureAndHonorsAgentPolicy(t *testing.T) {
	gateway := NewGateway()
	attempts := 0
	if err := gateway.Register(LocalTool{ToolName: "flaky", Handler: func(context.Context, map[string]any) (Result, error) {
		attempts++
		if attempts < 2 {
			return Result{}, fmt.Errorf("transient")
		}
		return Result{Content: "ok"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	gateway.Configure(Policy{Retries: 1, AgentAllowed: map[string]map[string]bool{"planner": {"flaky": true}}}, nil)
	ctx := WithAgentContext(context.Background(), "planner")
	if _, err := gateway.Call(ctx, "flaky", nil); err != nil || attempts != 2 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
	if _, err := gateway.Call(WithAgentContext(context.Background(), "reviewer"), "flaky", nil); err == nil {
		t.Fatal("reviewer should be denied")
	}
}

func TestMCPClientCallsJSONRPCTool(t *testing.T) {
	response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": "ok"}})
	client := NewMCPClient(strings.NewReader(string(response)+"\n"), &strings.Builder{})
	result, err := client.Call(context.Background(), "echo", map[string]any{"value": "ok"})
	if err != nil {
		t.Fatal(err)
	}
	content := result.Content.(map[string]any)["content"]
	if content != "ok" {
		t.Fatalf("content=%v", content)
	}
}

func TestMCPClientNegotiatesCapabilities(t *testing.T) {
	responses := "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"capabilities\":{\"tools\":{}}}}\n" + "{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"tools\":[{\"name\":\"echo\"}]}}\n"
	client := NewMCPClient(strings.NewReader(responses), &strings.Builder{})
	if _, err := client.Initialize(context.Background(), "test-client"); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0]["name"] != "echo" {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
}

func TestDocumentSidecarClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/parse" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"line_count":2,"chunks":[{"text":"hello"}]}`))
	}))
	defer server.Close()
	result, err := NewDocumentSidecar(server.URL).Call(context.Background(), map[string]any{"filename": "a.txt", "content": "hello\nworld"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content.(map[string]any)["line_count"].(float64) != 2 {
		t.Fatalf("result=%+v", result)
	}
}
