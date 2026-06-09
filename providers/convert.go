package providers

import (
	"encoding/json"
	"strings"

	"github.com/decodo/tyci-agent/api"
)

// RichMessagesToChat converts RichMessage slice to ChatMessage slice,
// optionally prepending a system message.
func RichMessagesToChat(msgs []RichMessage, system string) []api.ChatMessage {
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

		var textParts []string
		var toolCalls []api.ChatToolCall
		var toolCallID string

		for _, block := range m.Content {
			switch block.Type {
			case "text":
				textParts = append(textParts, block.Text)
				// Tool results are stored as text blocks with ToolCallID/ToolName
				if block.ToolCallID != "" {
					toolCallID = block.ToolCallID
				}
			case "thinking":
				// Skip thinking blocks for OpenAI
			case "toolCall":
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
			case "toolResult":
				textParts = append(textParts, block.Text)
				if block.ToolCallID != "" {
					toolCallID = block.ToolCallID
				}
			}
		}

		msg := api.ChatMessage{
			Role:    role,
			Content: strings.Join(textParts, ""),
		}

		if len(toolCalls) > 0 {
			msg.ToolCalls = toolCalls
		}
		if toolCallID != "" {
			msg.ToolCallID = toolCallID
		}

		result = append(result, msg)
	}
	return result
}

// RichMessagesToAnthropic converts RichMessage slice to AnthropicMessage slice.
func RichMessagesToAnthropic(msgs []RichMessage) []api.AnthropicMessage {
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

// RichMessagesToGemini converts RichMessage slice to Gemini content and system instruction.
// Returns contents and any system instruction text extracted from the messages.
func RichMessagesToGemini(msgs []RichMessage) ([]api.GeminiContent, string) {
	var contents []api.GeminiContent
	var systemText string
	for _, m := range msgs {
		role := m.Role
		// Map "toolResult" → "function" for Gemini (Gemini uses "function" role for tool results)
		if role == "toolResult" {
			role = "function"
		}
		// Map "system" role to system instruction
		if role == "system" {
			for _, block := range m.Content {
				if block.Type == "text" {
					systemText += block.Text
				}
			}
			continue
		}

		var parts []api.GeminiPart
		for _, block := range m.Content {
			switch block.Type {
			case "text":
				// For tool results with a ToolCallID, wrap as functionResponse
				if role == "function" && block.ToolCallID != "" {
					parts = append(parts, api.GeminiPart{
						FunctionResponse: &api.GeminiFunctionResponse{
							Name: block.ToolName,
							Response: struct {
								Name    string `json:"name"`
								Content string `json:"content"`
							}{
								Name:    block.ToolName,
								Content: block.Text,
							},
						},
					})
				} else {
					parts = append(parts, api.GeminiPart{Text: block.Text})
				}
			case "thinking":
				// Skip thinking blocks for Gemini
			case "toolCall":
				// Convert tool calls to functionCall parts for Gemini
				var args json.RawMessage
				if block.Arguments != nil {
					args = block.Arguments
				} else {
					args = json.RawMessage("{}")
				}
				parts = append(parts, api.GeminiPart{
					FunctionCall: &api.GeminiFunctionCall{
						Name: block.Name,
						Args: args,
					},
				})
			case "toolResult":
				// Convert tool results to functionResponse parts for Gemini
				parts = append(parts, api.GeminiPart{
					FunctionResponse: &api.GeminiFunctionResponse{
						Name: block.ToolName,
						Response: struct {
							Name    string `json:"name"`
							Content string `json:"content"`
						}{
							Name:    block.ToolName,
							Content: block.Text,
						},
					},
				})
			}
		}

		if len(parts) > 0 || role == "function" {
			contents = append(contents, api.GeminiContent{Parts: parts, Role: role})
		}
	}
	return contents, systemText
}
