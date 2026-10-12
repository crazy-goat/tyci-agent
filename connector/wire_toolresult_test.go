//go:build !noanthropic

package connector

import (
	"testing"
)

// TestEveryToolUseHasOneToolResult checks the wire formats of Anthropic and
// OpenAI-compatible providers. A history with a placeholder result and a
// later final result must give one result for each tool call.
func TestEveryToolUseHasOneToolResult(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "go"}}},
		{Role: "assistant", Content: []ContentBlock{
			{Type: "toolCall", ID: "t1", Name: "subagent", Arguments: []byte(`{}`)},
			{Type: "toolCall", ID: "t2", Name: "read", Arguments: []byte(`{}`)},
		}},
		{Role: "toolResult", Content: []ContentBlock{
			{Type: "text", Text: "started, id=c1", ToolCallID: "t1", ToolName: "subagent"},
			{Type: "text", Text: "file", ToolCallID: "t2", ToolName: "read"},
		}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "<tyci-notice kind=tool.result call_id=c1 failed=false duration_ms=5>done</tyci-notice>"}}},
	}

	uses := map[string]int{}
	results := map[string]int{}
	for _, m := range messagesToAnthropic(msgs) {
		for _, b := range m.Content {
			switch b.Type {
			case "tool_use":
				uses[b.ID]++
			case "tool_result":
				results[b.ToolUseID]++
			}
		}
	}
	checkPairs(t, "anthropic", uses, results)

	uses = map[string]int{}
	results = map[string]int{}
	for _, m := range messagesToChat(msgs, "") {
		for _, c := range m.ToolCalls {
			uses[c.ID]++
		}
		if m.Role == "tool" {
			results[m.ToolCallID]++
		}
	}
	checkPairs(t, "openai", uses, results)
}

func checkPairs(t *testing.T, name string, uses, results map[string]int) {
	t.Helper()
	if len(uses) != 2 {
		t.Fatalf("%s: tool uses = %v, want 2", name, uses)
	}
	for id := range uses {
		if uses[id] != 1 || results[id] != 1 {
			t.Errorf("%s: call %s has %d uses and %d results, want 1 and 1", name, id, uses[id], results[id])
		}
	}
}
