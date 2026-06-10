package mcp

import (
	"encoding/json"
	"testing"
)

func TestToolJSON(t *testing.T) {
	tool := Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
	}

	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("failed to marshal tool: %v", err)
	}

	var decoded Tool
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal tool: %v", err)
	}

	if decoded.Name != "test_tool" {
		t.Errorf("expected name 'test_tool', got %q", decoded.Name)
	}
	if decoded.Description != "A test tool" {
		t.Errorf("expected description 'A test tool', got %q", decoded.Description)
	}
}

func TestCallToolResultJSON(t *testing.T) {
	result := CallToolResult{
		Content: []ContentBlock{
			{Type: "text", Text: "Hello, world!"},
		},
		IsError: false,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal result: %v", err)
	}

	var decoded CallToolResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if len(decoded.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(decoded.Content))
	}
	if decoded.Content[0].Text != "Hello, world!" {
		t.Errorf("expected text 'Hello, world!', got %q", decoded.Content[0].Text)
	}
}

func TestRequestJSON(t *testing.T) {
	req := Request{
		JSONRPC: "2.0",
		Method:  "tools/list",
		ID:      1,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal request: %v", err)
	}

	if decoded.JSONRPC != "2.0" {
		t.Errorf("expected jsonrpc '2.0', got %q", decoded.JSONRPC)
	}
	if decoded.Method != "tools/list" {
		t.Errorf("expected method 'tools/list', got %q", decoded.Method)
	}
}

func TestResponseJSON(t *testing.T) {
	result := InitializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo: ServerInfo{
			Name:    "test-server",
			Version: "1.0.0",
		},
	}

	resultJSON, _ := json.Marshal(result)
	resp := Response{
		JSONRPC: "2.0",
		Result:  resultJSON,
		ID:      1,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}

	var decoded Response
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	var decodedResult InitializeResult
	if err := json.Unmarshal(decoded.Result, &decodedResult); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if decodedResult.ServerInfo.Name != "test-server" {
		t.Errorf("expected server name 'test-server', got %q", decodedResult.ServerInfo.Name)
	}
}

func TestRPCError(t *testing.T) {
	err := &RPCError{
		Code:    -32601,
		Message: "Method not found",
	}

	if err.Error() != "MCP RPC error -32601: Method not found" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestToolListRequest(t *testing.T) {
	req := ToolsListRequest(42)

	if req.Method != "tools/list" {
		t.Errorf("expected method 'tools/list', got %q", req.Method)
	}
	if req.ID != 42 {
		t.Errorf("expected id 42, got %d", req.ID)
	}
}

func TestToolsCallRequest(t *testing.T) {
	req := ToolsCallRequest(1, CallToolParams{
		Name:      "test",
		Arguments: json.RawMessage(`{"key":"value"}`),
	})

	if req.Method != "tools/call" {
		t.Errorf("expected method 'tools/call', got %q", req.Method)
	}

	var params CallToolParams
	paramsJSON, _ := json.Marshal(req.Params)
	json.Unmarshal(paramsJSON, &params)

	if params.Name != "test" {
		t.Errorf("expected name 'test', got %q", params.Name)
	}
}

func TestInitializeRequest(t *testing.T) {
	req := InitializeRequest(1)

	if req.Method != "initialize" {
		t.Errorf("expected method 'initialize', got %q", req.Method)
	}

	params, ok := req.Params.(InitializeParams)
	if !ok {
		t.Fatal("expected InitializeParams")
	}

	if params.ProtocolVersion != "2024-11-05" {
		t.Errorf("expected protocol version '2024-11-05', got %q", params.ProtocolVersion)
	}
	if params.ClientInfo.Name != "tyci-agent" {
		t.Errorf("expected client name 'tyci-agent', got %q", params.ClientInfo.Name)
	}
}
