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

	// Temperature, when non-nil, is forwarded to the provider's sampling
	// parameter. A pointer (not a plain float64) because 0 is a meaningful
	// value — "fully deterministic" — and must be distinguishable from
	// "the caller did not set it".
	//
	// Deliberately unvalidated here: acceptable ranges differ per provider
	// (Anthropic 0..1, OpenAI and Gemini 0..2) and connector/api is a dumb
	// transport layer. Range enforcement belongs to the server, which
	// returns a readable 400 — silently clamping here would mask a config
	// typo instead of surfacing it.
	Temperature *float64

	// MaxTokens caps the length of the model's reply. Zero means "not set":
	// Anthropic requires the field, so its connector substitutes a default;
	// the OpenAI and Gemini protocols treat it as optional, so their
	// connectors omit it entirely and the provider's own default applies.
	//
	// A plain int, not a pointer: unlike Temperature, 0 is not a meaningful
	// value here — a reply capped at zero tokens is not something anyone
	// asks for — so it can stand in for "unset" without ambiguity.
	MaxTokens int

	// NoPromptCache turns off provider-side prompt caching for this request.
	//
	// Negated on purpose: caching is the right default — it is the difference
	// between paying for the whole conversation every turn and paying for the
	// new part — so it must not depend on every call site remembering to ask
	// for it. The flag exists for an endpoint that rejects the cache_control
	// field, which some Anthropic-compatible gateways do.
	//
	// Only the Anthropic connector reads it. OpenAI-compatible providers cache
	// automatically with no field to set, and the Gemini connector does not
	// implement explicit caching.
	NoPromptCache bool
}
