package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

type MCPClient struct {
	reader *bufio.Reader
	writer io.Writer
	mu     sync.Mutex
	nextID int64
}

func NewMCPClient(reader io.Reader, writer io.Writer) *MCPClient {
	return &MCPClient{reader: bufio.NewReader(reader), writer: writer}
}

func (c *MCPClient) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	request := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(c.writer, "%s\n", data); err != nil {
		return nil, fmt.Errorf("write MCP request: %w", err)
	}
	result := make(chan struct {
		value json.RawMessage
		err   error
	}, 1)
	go func() {
		line, readErr := c.reader.ReadBytes('\n')
		if readErr != nil {
			result <- struct {
				value json.RawMessage
				err   error
			}{err: readErr}
			return
		}
		var response struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &response); err != nil {
			result <- struct {
				value json.RawMessage
				err   error
			}{err: err}
			return
		}
		if response.Error != nil {
			result <- struct {
				value json.RawMessage
				err   error
			}{err: fmt.Errorf("MCP error %d: %s", response.Error.Code, response.Error.Message)}
			return
		}
		result <- struct {
			value json.RawMessage
			err   error
		}{value: response.Result}
	}()
	select {
	case response := <-result:
		return response.value, response.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *MCPClient) Call(ctx context.Context, toolName string, arguments map[string]any) (Result, error) {
	value, err := c.Request(ctx, "tools/call", map[string]any{"name": toolName, "arguments": arguments})
	if err != nil {
		return Result{}, err
	}
	var output any
	if err := json.Unmarshal(value, &output); err != nil {
		return Result{}, err
	}
	return Result{Content: output}, nil
}

type MCPTool struct {
	Client    *MCPClient
	NameValue string
}

func (t MCPTool) Name() string { return t.NameValue }
func (t MCPTool) Call(ctx context.Context, arguments map[string]any) (Result, error) {
	if t.Client == nil {
		return Result{}, fmt.Errorf("MCP client is nil")
	}
	return t.Client.Call(ctx, t.NameValue, arguments)
}

func StartMCPCommand(ctx context.Context, command string, args ...string) (*exec.Cmd, *MCPClient, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, NewMCPClient(stdout, stdin), nil
}
