package connector

import (
	"context"
	"encoding/json"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/stream"
)

// anthropicMaxTokens is the max_tokens value sent on every Anthropic request.
// Anthropic requires the field; the value is the one this codebase has always
// used.
const anthropicMaxTokens = 4096

// anthropic speaks the Anthropic Messages protocol.
type anthropic struct {
	ep Endpoint
}

// NewAnthropic builds an Anthropic messages connector.
func NewAnthropic(ep Endpoint) (Connector, error) {
	return &anthropic{ep: ep}, nil
}

func (c *anthropic) Kind() string { return KindAnthropic }

func (c *anthropic) Stream(ctx context.Context, req Request, emit func(stream.Event) error) error {
	body := api.AnthropicRequest{
		Model:     req.Model,
		MaxTokens: anthropicMaxTokens,
		Stream:    true,
		System:    req.System,
		Messages:  messagesToAnthropic(req.Messages),
		// The tool-schema conversion stays in package api: it is also used by
		// api.AnthropicClient, and api/anthropic_stub.go has to keep a
		// build-tagged no-op counterpart. Moving it here would force changes
		// in api/, which Etap 2 explicitly avoids.
		Tools: api.ConvertToolsToAnthropic(req.Tools),
	}
	return api.StreamAnthropic(ctx, c.ep.APIKey, c.ep.URL(), body, emit)
}

// messagesToAnthropic converts a Message slice to an AnthropicMessage slice.
func messagesToAnthropic(msgs []Message) []api.AnthropicMessage {
	result := make([]api.AnthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		role := m.Role
		// Map "toolResult" → "user" for Anthropic API
		isToolResult := role == "toolResult"
		if isToolResult {
			role = "user"
		}

		var content []api.AnthropicContentBlock
		for _, block := range m.Content {
			// If this is a toolResult message, text blocks with ToolCallID
			// should be converted to tool_result blocks.
			if isToolResult && block.Type == "text" && block.ToolCallID != "" {
				content = append(content, api.AnthropicContentBlock{
					Type:      "tool_result",
					ToolUseID: block.ToolCallID,
					Content: []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}{{Type: "text", Text: block.Text}},
					IsError: block.IsError,
				})
				continue
			}

			switch block.Type {
			case "text":
				content = append(content, api.AnthropicContentBlock{
					Type: "text",
					Text: block.Text,
				})
			case "thinking":
				// Skip thinking blocks for Anthropic
			case "toolCall":
				var input any
				if block.Arguments != nil {
					var parsed any
					if err := json.Unmarshal(block.Arguments, &parsed); err == nil {
						input = parsed
					}
				}
				content = append(content, api.AnthropicContentBlock{
					Type:  "tool_use",
					ID:    block.ID,
					Name:  block.Name,
					Input: input,
				})
			case "toolResult":
				content = append(content, api.AnthropicContentBlock{
					Type:      "tool_result",
					ToolUseID: block.ToolCallID,
					Content: []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}{{Type: "text", Text: block.Text}},
					IsError: block.IsError,
				})
			}
		}
		result = append(result, api.AnthropicMessage{Role: role, Content: content})
	}
	return result
}
