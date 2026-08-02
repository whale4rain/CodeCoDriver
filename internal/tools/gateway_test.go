package tools

import (
	"context"
	"encoding/json"
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
