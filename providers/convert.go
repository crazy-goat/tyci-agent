package providers

import (
	"encoding/json"
	"strings"

	"github.com/decodo/tyci/api"
)

// RichMessagesToChat converts RichMessage slice to ChatMessage slice,
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

		// OpenAI (and strict-compatible providers) require one "tool" role
		// message per tool_call_id — a single message can't carry more than
		// one result. So each tool-result block in this RichMessage becomes
		// its own ChatMessage, mirroring what RichMessagesToAnthropic and
		// RichMessagesToGemini already do per-block. Blocks without a
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
