package connector

import (
	"context"
	"strings"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/stream"
)

// OptReasoning is the Endpoint option that enables the non-standard
// "reasoning": true field in chat-completion requests. It comes from
// ?reasoning=true in the provider URI.
const OptReasoning = "reasoning"

// openAI speaks the OpenAI chat-completions protocol, which most third-party
// providers (DeepSeek, GLM, Xiaomi, OpenRouter, ...) also expose. It is the
// fallback protocol for any api_type we do not recognize.
type openAI struct {
	ep        Endpoint
	reasoning bool
}

func init() { registerBuiltin(KindOpenAI, NewOpenAI) }

// NewOpenAI builds an OpenAI chat-completions connector.
func NewOpenAI(ep Endpoint) (Connector, error) {
	return &openAI{ep: ep, reasoning: ep.option(OptReasoning) == "true"}, nil
}

func (c *openAI) Kind() string { return KindOpenAI }

func (c *openAI) Stream(ctx context.Context, req Request, emit func(stream.Event) error) error {
	body := api.ChatRequest{
		Model:       req.Model,
		Stream:      true,
		Messages:    messagesToChat(req.Messages, req.System),
		Tools:       req.Tools,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	// Only send the reasoning field when ?reasoning=true was in the URI.
	if c.reasoning {
		body.Reasoning = true
	}
	s := api.ChatStreamer{HTTP: c.ep.HTTP, Headers: c.ep.Headers}
	return s.Stream(ctx, c.ep.APIKey, c.ep.URL(), body, emit)
}

// messagesToChat converts a Message slice to a ChatMessage slice,
// optionally prepending a system message.
//
// It sanitizes tool-related fields to stay compatible with strict OpenAI
// providers (e.g. DeepSeek, GLM, Xiaomi) that reject:
//   - assistant messages whose tool_calls[].function.name is empty,
//   - "tool" role messages without a tool_call_id.
//
// Tool calls with an empty name are dropped (a model emitting a tool_call
// without a name is malformed and cannot be dispatched). Tool results
// without a tool_call_id are dropped entirely — an orphan tool message
// has no matching assistant tool_call and breaks the conversation pairing.
func messagesToChat(msgs []Message, system string) []api.ChatMessage {
	result := make([]api.ChatMessage, 0, len(msgs)+1)
	if system != "" {
		result = append(result, api.ChatMessage{Role: "system", Content: system})
	}
	for _, m := range msgs {
		role := m.Role
		// Map "toolResult" → "tool" for OpenAI API
		if role == "toolResult" {
			role = "tool"
		}

		// OpenAI (and strict-compatible providers) require one "tool" role
		// message per tool_call_id — a single message can't carry more than
		// one result. So each tool-result block in this Message becomes
		// its own ChatMessage, mirroring what messagesToAnthropic and
		// messagesToGemini already do per-block. Blocks without a
		// ToolCallID are dropped: strict providers reject "tool" messages
		// missing `tool_call_id`, and the pairing is already broken (no
		// matching assistant tool_call).
		if role == "tool" {
			for _, block := range m.Content {
				isToolResult := block.Type == "toolResult" ||
					// Tool results are also stored as text blocks with
					// ToolCallID/ToolName (see agent/run_tools.go).
					(block.Type == "text" && block.ToolCallID != "")
				if !isToolResult || block.ToolCallID == "" {
					continue
				}
				result = append(result, api.ChatMessage{
					Role:       role,
					Content:    block.Text,
					ToolCallID: block.ToolCallID,
				})
			}
			continue
		}

		var textParts []string
		var toolCalls []api.ChatToolCall

		for _, block := range m.Content {
			switch block.Type {
			case "text":
				textParts = append(textParts, block.Text)
			case "thinking":
				// Skip thinking blocks for OpenAI
			case "toolCall":
				// Drop malformed tool calls without a function name. A
				// tool_call with no name cannot be dispatched and triggers
				// "tool_calls[0] is missing a function name" 400s on
				// strict providers (GLM, DeepSeek, OpenAI).
				if block.Name == "" {
					continue
				}
				args := ""
				if block.Arguments != nil {
					args = string(block.Arguments)
				}
				toolCalls = append(toolCalls, api.ChatToolCall{
					ID:   block.ID,
					Type: "function",
					Function: api.ChatFunctionCall{
						Name:      block.Name,
						Arguments: args,
					},
				})
			}
		}

		msg := api.ChatMessage{
			Role:    role,
			Content: strings.Join(textParts, ""),
		}

		if len(toolCalls) > 0 {
			msg.ToolCalls = toolCalls
		}

		result = append(result, msg)
	}
	return result
}
