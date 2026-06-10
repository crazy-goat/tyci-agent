// Package mcp implements the Model Context Protocol (MCP) client.
//
// MCP is a JSON-RPC 2.0 based protocol for communication between
// AI agents and external tool servers. This package provides:
//   - Protocol message types (JSON-RPC 2.0)
//   - MCP-specific types (Tool, CallToolResult)
//   - StdioClient for spawning child processes
//   - Config loading from ~/.tyci/mcp.json
package mcp

import (
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 message types.

// Request represents a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      int         `json:"id"`
}

// Response represents a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      int             `json:"id"`
}

// RPCError represents a JSON-RPC 2.0 error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("MCP RPC error %d: %s", e.Code, e.Message)
}

// MCP protocol types.

// Tool represents an MCP tool definition.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// CallToolParams represents parameters for tools/call.
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// CallToolResult represents the result of a tool call.
type CallToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock represents a content block in an MCP response.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolListResult represents the result of tools/list.
type ToolListResult struct {
	Tools []Tool `json:"tools"`
}

// InitializeParams represents parameters for the initialize request.
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    ClientCaps     `json:"capabilities"`
	ClientInfo      ClientInfo     `json:"clientInfo"`
}

// ClientInfo contains client identification.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientCaps represents client capabilities.
type ClientCaps struct {
	Tools any `json:"tools,omitempty"`
}

// InitializeResult represents the result of the initialize request.
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    ServerCaps     `json:"capabilities"`
	ServerInfo      ServerInfo     `json:"serverInfo"`
}

// ServerInfo contains server identification.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerCaps represents server capabilities.
type ServerCaps struct {
	Tools any `json:"tools,omitempty"`
}

// ToolsListRequest creates a tools/list request.
func ToolsListRequest(id int) Request {
	return Request{
		JSONRPC: "2.0",
		Method:  "tools/list",
		ID:      id,
	}
}

// ToolsCallRequest creates a tools/call request.
func ToolsCallRequest(id int, params CallToolParams) Request {
	return Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      id,
	}
}

// InitializeRequest creates an initialize request.
func InitializeRequest(id int) Request {
	return Request{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params: InitializeParams{
			ProtocolVersion: "2024-11-05",
			Capabilities:    ClientCaps{},
			ClientInfo: ClientInfo{
				Name:    "tyci-agent",
				Version: "0.1.0",
			},
		},
		ID: id,
	}
}

// InitializedNotification creates an notifications/initialized notification.
func InitializedNotification() Request {
	return Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
}
