//go:build !noanthropic

package connector

import (
	"context"
	"encoding/json"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/stream"
)

// anthropicDefaultMaxTokens is the max_tokens sent when Request.MaxTokens is
// unset. Anthropic requires the field, so unlike the other protocols there is
// no "let the provider decide" option here.
//
// 4096 is deliberately conservative rather than generous. Anthropic rejects a
// max_tokens above the model's own output limit with a 400, and that limit
// differs per model — 4096 on claude-3 Opus and Haiku, far more on the
// current ones — while the provider catalog this project reads (models.dev)
// carries no output limits to check against. A too-small default truncates a
// long reply; a too-large one makes the request fail outright on an older
// model, which is worse. Raise it for the models you actually use: agents.json
// "max_tokens", or "max_tokens" in an agent definition's frontmatter.
const anthropicDefaultMaxTokens = 4096

// anthropic speaks the Anthropic Messages protocol.
type anthropic struct {
	ep Endpoint
}

func init() { registerBuiltin(KindAnthropic, NewAnthropic) }

// NewAnthropic builds an Anthropic messages connector.
func NewAnthropic(ep Endpoint) (Connector, error) {
	return &anthropic{ep: ep}, nil
}

func (c *anthropic) Kind() string { return KindAnthropic }

func (c *anthropic) Stream(ctx context.Context, req Request, emit func(stream.Event) error) error {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	messages := messagesToAnthropic(req.Messages)
	// The tool-schema conversion still lives in package api next to the SSE
	// parser it belongs to; both are behind the same !noanthropic tag, so
	// this file and api/anthropic.go appear and disappear together.
	tools := api.ConvertToolsToAnthropic(req.Tools)
	system := systemToAnthropic(req.System)

	if !req.NoPromptCache {
		// Three breakpoints, in the order Anthropic reads the prefix: tools,
		// system, then the conversation. Each marks "everything up to here is
		// the same as last time", and on an agent's turn that is nearly all
		// of the input — the schemas and the system prompt never change at
		// all, and the conversation only grows at the end.
		tools = api.MarkLastToolCached(tools)
		system = api.MarkLastSystemBlockCached(system)
		markLastMessageCached(messages)
	}

	body := api.AnthropicRequest{
		Model:       req.Model,
		MaxTokens:   maxTokens,
		Stream:      true,
		System:      system,
		Messages:    messages,
		Tools:       tools,
		Temperature: req.Temperature,
	}
	s := api.AnthropicStreamer{HTTP: c.ep.HTTP, Headers: c.ep.Headers}
	return s.Stream(ctx, c.ep.APIKey, c.ep.URL(), body, emit)
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

// systemToAnthropic wraps the system prompt in the block form. Always the
// list, never the bare string: only the list can carry cache_control, and
// switching shapes depending on whether caching is on would mean two request
// bodies to reason about instead of one.
func systemToAnthropic(system string) []api.AnthropicSystemBlock {
	if system == "" {
		return nil
	}
	return []api.AnthropicSystemBlock{{Type: "text", Text: system}}
}

// markLastMessageCached puts the conversation breakpoint on the very last
// content block of the very last message.
//
// That position is what makes the cache pay for itself across turns: the
// prefix it closes is the entire conversation so far, which the next request
// re-sends verbatim and can therefore read back instead of re-processing. A
// breakpoint anywhere earlier would leave the bulk of a long session outside
// the cache.
func markLastMessageCached(msgs []api.AnthropicMessage) {
	for i := len(msgs) - 1; i >= 0; i-- {
		blocks := msgs[i].Content
		if len(blocks) == 0 {
			continue
		}
		blocks[len(blocks)-1].CacheControl = api.CacheEphemeral()
		return
	}
}
