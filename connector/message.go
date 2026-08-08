package connector

import "encoding/json"

// ContentBlock represents a single content block within a Message.
type ContentBlock struct {
	Type     string `json:"type"` // "text", "thinking", "toolCall", "toolResult"
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`

	// Tool call fields
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`

	// Tool result fields
	IsError    bool   `json:"isError,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
}

// Message is the canonical message type used throughout the agent loop.
// It carries structured content blocks instead of a flat text string,
// allowing connectors to build their own wire format.
//
// It lives here rather than in providers because providers imports connector;
// providers.RichMessage is an alias of this type.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// Request is the protocol-agnostic model request handed to a Connector.
type Request struct {
	Model    string
	System   string
	Messages []Message
	Tools    json.RawMessage
	Debug    bool
}
