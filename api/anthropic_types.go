//go:build !noanthropic

package api

import "encoding/json"

// AnthropicCacheControl marks a block as the end of a cacheable prefix.
//
// Anthropic caches the request prefix up to each marked block. A later request
// whose prefix is byte-identical up to that point reads it back instead of
// re-processing it, which is most of an agent's input on every turn after the
// first: the tool schemas, the system prompt and the conversation so far never
// change, yet they are re-sent in full each time.
type AnthropicCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// CacheEphemeral is the only cache type in use, hoisted so the three places
// that mark a breakpoint cannot disagree about the spelling.
func CacheEphemeral() *AnthropicCacheControl {
	return &AnthropicCacheControl{Type: "ephemeral"}
}

// AnthropicSystemBlock is one block of the system prompt. Anthropic accepts
// the system prompt either as a plain string or as a list of blocks, and only
// the list form can carry cache_control — so this codebase always sends the
// list, whether caching is on or not, rather than switching shapes.
type AnthropicSystemBlock struct {
	Type         string                 `json:"type"` // "text"
	Text         string                 `json:"text"`
	CacheControl *AnthropicCacheControl `json:"cache_control,omitempty"`
}

// AnthropicContentBlock is a generic content block for Anthropic messages.
type AnthropicContentBlock struct {
	Type  string `json:"type"` // "text", "tool_use", "tool_result"
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`

	// Tool result fields
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
	IsError bool `json:"is_error,omitempty"`

	// CacheControl, when set, ends a cacheable prefix at this block. Set on
	// the last block of the last message, so that everything said so far in
	// the conversation is cached for the next turn to read back.
	CacheControl *AnthropicCacheControl `json:"cache_control,omitempty"`
}

type AnthropicMessage struct {
	Role    string                  `json:"role"`
	Content []AnthropicContentBlock `json:"content"`
}

type AnthropicRequest struct {
	Model     string                 `json:"model"`
	MaxTokens int                    `json:"max_tokens"`
	Stream    bool                   `json:"stream"`
	System    []AnthropicSystemBlock `json:"system,omitempty"`
	Messages  []AnthropicMessage     `json:"messages"`
	Tools     json.RawMessage        `json:"tools,omitempty"`
	// Temperature is a pointer so the zero value (fully deterministic
	// sampling) can be sent explicitly; omitempty on a *float64 only omits
	// a nil pointer, never a pointer to 0. See connector.Request.Temperature
	// for why this layer never validates or clamps the value.
	Temperature *float64 `json:"temperature,omitempty"`
}
