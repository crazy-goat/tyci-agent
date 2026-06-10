package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPClientName(t *testing.T) {
	c := NewHTTPClient("test-server", "http://localhost:8080", "")
	if c.Name() != "test-server" {
		t.Errorf("expected name 'test-server', got %q", c.Name())
	}
}

func TestHTTPClientClose(t *testing.T) {
	c := NewHTTPClient("test", "http://localhost:8080", "")
	if err := c.Close(); err != nil {
		t.Errorf("Close() returned error: %v", err)
	}
}

func TestHTTPClientInitialize(t *testing.T) {
	// Mock MCP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.Method == "initialize" {
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
				ID:      req.ID,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		} else if req.Method == "notifications/initialized" {
			// Notifications don't get responses
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	c := NewHTTPClient("test", server.URL, "")
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}
}

func TestHTTPClientListTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)

		if req.Method == "tools/list" {
			result := ToolListResult{
				Tools: []Tool{
					{
						Name:        "test_tool",
						Description: "A test tool",
						InputSchema: json.RawMessage(`{"type":"object"}`),
					},
				},
			}
			resultJSON, _ := json.Marshal(result)
			resp := Response{
				JSONRPC: "2.0",
				Result:  resultJSON,
				ID:      req.ID,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	c := NewHTTPClient("test", server.URL, "")

	// Manually set nextID for predictable test
	c.mu.Lock()
	c.nextID = 1
	c.mu.Unlock()

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error: %v", err)
	}

	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "test_tool" {
		t.Errorf("expected tool name 'test_tool', got %q", tools[0].Name)
	}
}

func TestHTTPClientCallTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)

		if req.Method == "tools/call" {
			result := CallToolResult{
				Content: []ContentBlock{
					{Type: "text", Text: "Tool executed successfully"},
				},
			}
			resultJSON, _ := json.Marshal(result)
			resp := Response{
				JSONRPC: "2.0",
				Result:  resultJSON,
				ID:      req.ID,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	c := NewHTTPClient("test", server.URL, "")
	c.mu.Lock()
	c.nextID = 1
	c.mu.Unlock()

	result, err := c.CallTool(context.Background(), "test_tool", json.RawMessage(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("CallTool() error: %v", err)
	}

	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Text != "Tool executed successfully" {
		t.Errorf("expected text 'Tool executed successfully', got %q", result.Content[0].Text)
	}
}

func TestHTTPClientSSEResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send SSE event with response
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not support Flusher")
			return
		}

		result := ToolListResult{
			Tools: []Tool{
				{Name: "sse_tool", Description: "SSE tool"},
			},
		}
		resultJSON, _ := json.Marshal(result)
		resp := Response{
			JSONRPC: "2.0",
			Result:  resultJSON,
			ID:      1,
		}
		respJSON, _ := json.Marshal(resp)

		// Write SSE event
		for _, line := range strings.Split(string(respJSON), "\n") {
			if line != "" {
				w.Write([]byte("data: " + line + "\n"))
			}
		}
		w.Write([]byte("\n"))
		flusher.Flush()
	}))
	defer server.Close()

	c := NewHTTPClient("test", server.URL, "")
	c.mu.Lock()
	c.nextID = 1
	c.mu.Unlock()

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error: %v", err)
	}

	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "sse_tool" {
		t.Errorf("expected tool name 'sse_tool', got %q", tools[0].Name)
	}
}

func TestHTTPClientAuthToken(t *testing.T) {
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")

		var req Request
		json.NewDecoder(r.Body).Decode(&req)

		result := ToolListResult{Tools: []Tool{}}
		resultJSON, _ := json.Marshal(result)
		resp := Response{
			JSONRPC: "2.0",
			Result:  resultJSON,
			ID:      req.ID,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewHTTPClient("test", server.URL, "bearer")
	c.SetAuthToken("secret-token-123")
	c.mu.Lock()
	c.nextID = 1
	c.mu.Unlock()

	_, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error: %v", err)
	}

	if receivedAuth != "Bearer secret-token-123" {
		t.Errorf("expected 'Bearer secret-token-123', got %q", receivedAuth)
	}
}

func TestNewClientWithURL(t *testing.T) {
	cfg := ServerConfig{
		URL:  "https://mcp.example.com/sse",
		Auth: "bearer",
	}

	client, err := NewClient("remote", cfg)
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}

	if _, ok := client.(*HTTPClient); !ok {
		t.Errorf("expected *HTTPClient, got %T", client)
	}

	if client.Name() != "remote" {
		t.Errorf("expected name 'remote', got %q", client.Name())
	}
}

func TestNewClientWithCommand(t *testing.T) {
	cfg := ServerConfig{
		Command: "echo",
		Args:    []string{"test"},
	}

	client, err := NewClient("local", cfg)
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}

	if _, ok := client.(*StdioClient); !ok {
		t.Errorf("expected *StdioClient, got %T", client)
	}
}

func TestNewClientInvalid(t *testing.T) {
	cfg := ServerConfig{}

	_, err := NewClient("invalid", cfg)
	if err == nil {
		t.Error("expected error for empty config")
	}
}

func TestConfigWithAuth(t *testing.T) {
	cfgJSON := `{
		"mcpServers": {
			"remote": {
				"url": "https://mcp.example.com/sse",
				"auth": "bearer"
			}
		}
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		t.Fatalf("failed to unmarshal config: %v", err)
	}

	remote, ok := cfg.MCPServers["remote"]
	if !ok {
		t.Fatal("expected 'remote' server in config")
	}

	if remote.URL != "https://mcp.example.com/sse" {
		t.Errorf("expected URL 'https://mcp.example.com/sse', got %q", remote.URL)
	}
	if remote.Auth != "bearer" {
		t.Errorf("expected auth 'bearer', got %q", remote.Auth)
	}
}
