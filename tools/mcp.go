package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/decodo/tyci/internal/mcp"
)

// MCPToolRunner routes tool calls to MCP servers.
type MCPToolRunner struct {
	mu      sync.RWMutex
	clients map[string]mcp.Client
	tools   map[string]*mcpTool // prefixed name -> tool info

	// Sampling/Elicitation handlers
	samplingFunc  func(ctx context.Context, messages []mcp.SamplingMessage, model string) (string, error)
	elicitationFunc func(ctx context.Context, message string) (string, error)
}

type mcpTool struct {
	server string
	tool   mcp.Tool
}

// NewMCPToolRunner creates a new MCP tool runner.
func NewMCPToolRunner() *MCPToolRunner {
	return &MCPToolRunner{
		clients: make(map[string]mcp.Client),
		tools:   make(map[string]*mcpTool),
	}
}

// Connect connects to all configured MCP servers.
func (r *MCPToolRunner) Connect(ctx context.Context) error {
	clients, err := mcp.ConnectAll(ctx)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for name, client := range clients {
		r.clients[name] = client

		// Set up sampling handler
		client.SetSamplingHandler(r.makeSamplingHandler(name))

		// Set up elicitation handler
		client.SetElicitationHandler(r.makeElicitationHandler(name))

		// List tools from this server
		tools, err := client.ListTools(ctx)
		if err != nil {
			fmt.Printf("Warning: failed to list tools from MCP server %q: %v\n", name, err)
			continue
		}

		// Register tools with prefix
		for _, tool := range tools {
			prefixedName := fmt.Sprintf("mcp_%s_%s", name, tool.Name)
			r.tools[prefixedName] = &mcpTool{
				server: name,
				tool:   tool,
			}
		}
	}

	return nil
}

// SetSamplingFunc sets the function to handle sampling requests.
func (r *MCPToolRunner) SetSamplingFunc(f func(ctx context.Context, messages []mcp.SamplingMessage, model string) (string, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samplingFunc = f
}

// SetElicitationFunc sets the function to handle elicitation requests.
func (r *MCPToolRunner) SetElicitationFunc(f func(ctx context.Context, message string) (string, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.elicitationFunc = f
}

// makeSamplingHandler creates a SamplingHandler for a specific server.
func (r *MCPToolRunner) makeSamplingHandler(serverName string) mcp.SamplingHandler {
	return func(ctx context.Context, srvName string, req *mcp.SamplingRequest) (*mcp.SamplingResult, error) {
		r.mu.RLock()
		handler := r.samplingFunc
		r.mu.RUnlock()

		if handler == nil {
			return nil, fmt.Errorf("sampling not supported")
		}

		// Determine model to use
		model := ""
		if req.ModelPreferences != nil && len(req.ModelPreferences.Hints) > 0 {
			model = req.ModelPreferences.Hints[0].Name
		}

		// Call the handler
		response, err := handler(ctx, req.Messages, model)
		if err != nil {
			return nil, err
		}

		// Build result
		content, _ := json.Marshal(mcp.SamplingResultContent{
			Type: "text",
			Text: response,
		})

		return &mcp.SamplingResult{
			Model:      model,
			Role:       "assistant",
			Content:    content,
			StopReason: "endTurn",
		}, nil
	}
}

// makeElicitationHandler creates an ElicitationHandler for a specific server.
func (r *MCPToolRunner) makeElicitationHandler(serverName string) mcp.ElicitationHandler {
	return func(ctx context.Context, srvName string, req *mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
		r.mu.RLock()
		handler := r.elicitationFunc
		r.mu.RUnlock()

		if handler == nil {
			return nil, fmt.Errorf("elicitation not supported")
		}

		// Call the handler
		response, err := handler(ctx, req.Message)
		if err != nil {
			return nil, err
		}

		// Build result
		content, _ := json.Marshal(map[string]interface{}{
			"type": "text",
			"text": response,
		})

		return &mcp.ElicitationResult{
			Action:  "accept",
			Content: content,
		}, nil
	}
}

// Close shuts down all MCP clients.
func (r *MCPToolRunner) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, client := range r.clients {
		client.Close()
	}
}

// HasTool returns true if the tool name is an MCP tool.
func (r *MCPToolRunner) HasTool(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// RunTool executes an MCP tool.
func (r *MCPToolRunner) RunTool(ctx context.Context, name string, arguments map[string]any) ToolResult {
	r.mu.RLock()
	t, ok := r.tools[name]
	client := r.clients[t.server]
	r.mu.RUnlock()

	if !ok || client == nil {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   fmt.Sprintf("MCP tool %q not found", name),
		}
	}

	// Marshal arguments
	argsJSON, err := json.Marshal(arguments)
	if err != nil {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   fmt.Sprintf("marshaling arguments: %v", err),
		}
	}

	// Call the tool
	result, err := client.CallTool(ctx, t.tool.Name, argsJSON)
	if err != nil {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   fmt.Sprintf("MCP tool error: %v", err),
		}
	}

	// Extract text content
	var content strings.Builder
	for _, block := range result.Content {
		if block.Type == "text" {
			content.WriteString(block.Text)
		}
	}

	if result.IsError {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   content.String(),
		}
	}

	return ToolResult{
		Type:    "result",
		Success: true,
		Content: content.String(),
	}
}

// MCPToolsSchema returns tool definitions for all MCP tools.
func (r *MCPToolRunner) MCPToolsSchema() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var schema []map[string]any
	for name, t := range r.tools {
		schema = append(schema, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": t.tool.Description,
				"parameters":  t.tool.InputSchema,
			},
		})
	}
	return schema
}

// globalMCPRunner is the global MCP tool runner.
var globalMCPRunner *MCPToolRunner

// InitMCP initializes the global MCP tool runner.
func InitMCP(ctx context.Context) error {
	runner := NewMCPToolRunner()
	if err := runner.Connect(ctx); err != nil {
		return err
	}
	globalMCPRunner = runner
	return nil
}

// GetMCPToolRunner returns the global MCP tool runner.
func GetMCPToolRunner() *MCPToolRunner {
	return globalMCPRunner
}
