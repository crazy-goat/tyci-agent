package connector

import "testing"

// TestOpenAIWireOneToolResultPerToolUse: the OpenAI-compatible wire form gives
// exactly one tool message for each tool call. A late tool.result notice is a
// user message and does not add a second answer.
func TestOpenAIWireOneToolResultPerToolUse(t *testing.T) {
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
	for _, m := range messagesToChat(msgs, "") {
		for _, c := range m.ToolCalls {
			uses[c.ID]++
		}
		if m.Role == "tool" {
			results[m.ToolCallID]++
		}
	}
	if len(uses) != 2 {
		t.Fatalf("tool uses = %v, want 2", uses)
	}
	for id := range uses {
		if uses[id] != 1 || results[id] != 1 {
			t.Errorf("call %s has %d uses and %d results, want 1 and 1", id, uses[id], results[id])
		}
	}
}
