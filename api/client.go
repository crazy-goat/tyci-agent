package api

import (
	"context"

	"github.com/decodo/tyci/stream"
)

// Streamer is the common interface for all API streaming clients.
// Each provider (OpenAI/Chat, Anthropic, Gemini) implements this interface.
type Streamer interface {
	// Stream sends a streaming request to the API and emits events via the emit callback.
	Stream(ctx context.Context, req *StreamRequest, emit EmitFunc) error
}

// StreamRequest is the unified request structure for all API providers.
// Each client implementation will translate this to its specific format.
type StreamRequest struct {
	// Model is the model identifier (e.g., "gpt-4", "claude-3-opus", "gemini-pro")
	Model string

	// Messages is the conversation history
	Messages []Message

	// Tools are the tool definitions in OpenAI format
	Tools []ToolDef

	// Temperature controls randomness (0.0-2.0)
	Temperature float64

	// MaxTokens limits the response length
	MaxTokens int

	// System is the system prompt
	System string

	// Provider-specific fields can be added as needed
}

// Message represents a single message in the conversation
type Message struct {
	Role       string      `json:"role"`
	Content    string      `json:"content"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
}

// ToolDef represents a tool definition
type ToolDef struct {
	Type     string         `json:"type"`
	Function ToolDefinition `json:"function"`
}

// ToolDefinition contains the tool's metadata
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// ToolCall represents a tool call from the model
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall contains the function name and arguments
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolResult contains the result of a tool execution
type ToolResult struct {
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

// StreamEvent is used by the emit callback to send streaming events
type StreamEvent = stream.Event

// EmitFunc is the callback type for streaming events
type EmitFunc = func(StreamEvent) error
