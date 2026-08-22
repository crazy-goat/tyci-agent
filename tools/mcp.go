package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/decodo/tyci/internal/mcp"
)

// mcpConnectTimeout bounds how long Connect waits for a single configured
// MCP server to finish its handshake and list its tools during startup.
// See internal/mcp.ConnectAllTimeout: a server that is missing, slow, or
// broken degrades to "unavailable" with one warning rather than blocking
// (or crashing) the session.
var mcpConnectTimeout = 5 * time.Second

// MCPToolRunner routes tool calls to MCP servers.
type MCPToolRunner struct {
	mu      sync.RWMutex
	clients map[string]mcp.Client
	tools   map[string]*mcpTool // prefixed name -> tool info

	// Sampling/Elicitation handlers
	samplingFunc    func(ctx context.Context, messages []mcp.SamplingMessage, model string) (string, error)
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

// Connect connects to all configured MCP servers, giving each one up to
// mcpConnectTimeout to finish its handshake and list its tools (see
// internal/mcp.ConnectAllTimeout). A server that errors or doesn't answer
// in time is skipped — reported once, on stderr, by ConnectAllTimeout
// itself — rather than failing this call or blocking the others.
func (r *MCPToolRunner) Connect(ctx context.Context) error {
	servers, err := mcp.ConnectAllTimeout(ctx, mcpConnectTimeout)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for name, srv := range servers {
		r.clients[name] = srv.Client

		// Set up sampling handler
		srv.Client.SetSamplingHandler(r.makeSamplingHandler(name))

		// Set up elicitation handler
		srv.Client.SetElicitationHandler(r.makeElicitationHandler(name))

		// Register tools with prefix
		for _, tool := range srv.Tools {
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

// Close shuts down all MCP clients concurrently. Each StdioClient.Close can
// take up to its own grace period to force-kill an unresponsive server; if
// this ran serially while holding r.mu, closing three such servers would
// stall the caller (and every RunTool/HasTool/MCPToolsSchema call, which
// all need r.mu) for the sum of their grace periods instead of the max. We
// snapshot the client map under the lock, then release it before closing,
// the same pattern StdioClient.Close itself uses for its own process wait.
func (r *MCPToolRunner) Close() {
	r.mu.RLock()
	clients := make([]mcp.Client, 0, len(r.clients))
	for _, client := range r.clients {
		clients = append(clients, client)
	}
	r.mu.RUnlock()

	var wg sync.WaitGroup
	for _, client := range clients {
		wg.Add(1)
		go func(c mcp.Client) {
			defer wg.Done()
			c.Close()
		}(client)
	}
	wg.Wait()
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
	if !ok {
		r.mu.RUnlock()
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   fmt.Sprintf("MCP tool %q not found", name),
		}
	}
	client := r.clients[t.server]
	r.mu.RUnlock()

	if client == nil {
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

// MCPToolsSchema returns tool definitions for all MCP tools, sorted by name.
// A stable order lets repeated calls (e.g. two spawns of the same subagent)
// produce byte-identical schemas, which is required to share a provider-side
// prompt-cache prefix; iterating the map directly would randomize the order
// on every call.
func (r *MCPToolRunner) MCPToolsSchema() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	var schema []map[string]any
	for _, name := range names {
		t := r.tools[name]
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

// SetMCPToolRunnerForTests overrides the global MCP tool runner and returns
// a func that restores whatever was there before. Test-only escape hatch
// for callers outside this package (e.g. package main's initCommon tests)
// that need deterministic before/after state around a real InitMCP call —
// mirrors the same pattern as internal/connect.SetHTTPClientForTests.
func SetMCPToolRunnerForTests(r *MCPToolRunner) (restore func()) {
	orig := globalMCPRunner
	globalMCPRunner = r
	return func() { globalMCPRunner = orig }
}

// ShutdownMCP closes every connected MCP server's client (killing stdio
// child processes, closing HTTP connections) and is a no-op when MCP was
// never initialized. Callers that connect MCP (see InitMCP) must defer this
// so a server's child process never outlives the tyci process that started
// it.
func ShutdownMCP() {
	if r := GetMCPToolRunner(); r != nil {
		r.Close()
	}
}

// mcpWildcardPrefix reports whether entry is a per-server MCP wildcard an
// agent definition can opt into (e.g. "mcp_weather_*"), and if so returns
// the prefix it grants ("mcp_weather_"). A subagent's tools: whitelist is
// authored before any server connects, so it can never spell out a dynamic
// mcp_<server>_<tool> name for every tool a server happens to expose today
// -- but the author can still know (or guess) the server's own name and
// opt into all of its tools with one line, rather than being forced to
// choose between "list none of them" and "list everything any server ever
// connects".
func mcpWildcardPrefix(entry string) (prefix string, ok bool) {
	if !strings.HasPrefix(entry, "mcp_") || !strings.HasSuffix(entry, "_*") {
		return "", false
	}
	return strings.TrimSuffix(entry, "*"), true
}

// mcpAllowedByWildcard reports whether name (an "mcp_<server>_<tool>" tool
// name) is granted by any mcp_<server>_* wildcard present in allowed.
func mcpAllowedByWildcard(name string, allowed []string) bool {
	for _, entry := range allowed {
		if prefix, ok := mcpWildcardPrefix(entry); ok && strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
