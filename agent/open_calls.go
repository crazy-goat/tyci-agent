package agent

import "github.com/crazy-goat/tyci-agent/connector"

// CloseOpenToolCalls adds a failed toolResult for every tool call that has
// no result. A session that was restarted leaves such calls open, and the
// provider rejects a tool call without a result. Each result is placed right
// after the assistant message that holds its call, so the order of the
// conversation stays valid.
func CloseOpenToolCalls(msgs []connector.Message) []connector.Message {
	answered := make(map[string]bool)
	for _, m := range msgs {
		for _, b := range m.Content {
			if m.Role == "toolResult" {
				answered[b.ToolCallID] = true
			}
		}
	}
	out := make([]connector.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m)
		if m.Role != "assistant" {
			continue
		}
		for _, b := range m.Content {
			if b.Type != "toolCall" || answered[b.ID] {
				continue
			}
			answered[b.ID] = true
			out = append(out, connector.Message{
				Role: "toolResult",
				Content: []connector.ContentBlock{{
					Type:       "text",
					Text:       "cancelled: session was restarted",
					ToolCallID: b.ID,
					ToolName:   b.Name,
					IsError:    true,
				}},
			})
		}
	}
	return out
}
