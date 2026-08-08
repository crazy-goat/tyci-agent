package providers

import (
	"testing"
)

// TestRichMessagesToChat_toolResult_splitsPerBlock covers the bug where a
// single toolResult RichMessage carrying multiple tool-result blocks used to
// collapse into exactly one "tool" role ChatMessage (concatenated content,
// last tool_call_id wins). Each block must now produce its own message.
func TestRichMessagesToChat_toolResult_splitsPerBlock(t *testing.T) {
	msgs := []RichMessage{
		{
			Role: "toolResult",
			Content: []ContentBlock{
				{Type: "toolResult", Text: "18C, cloudy", ToolCallID: "call_1", ToolName: "get_weather"},
				{Type: "toolResult", Text: "upstream weather service exploded", ToolCallID: "call_2", ToolName: "get_weather", IsError: true},
			},
		},
	}

	got := RichMessagesToChat(msgs, "")

	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(got), got)
	}
	if got[0].Role != "tool" || got[0].Content != "18C, cloudy" || got[0].ToolCallID != "call_1" {
		t.Errorf("message 0 = %+v, want role=tool content=%q tool_call_id=call_1", got[0], "18C, cloudy")
	}
	if got[1].Role != "tool" || got[1].Content != "upstream weather service exploded" || got[1].ToolCallID != "call_2" {
		t.Errorf("message 1 = %+v, want role=tool content=%q tool_call_id=call_2", got[1], "upstream weather service exploded")
	}
}

// TestRichMessagesToChat_toolResult_textRepresentation_splitsPerBlock covers
// the same fan-out, but for the "text" block shape with a non-empty
// ToolCallID — the representation agent/run_tools.go actually emits in
// production (see appendToolResults).
func TestRichMessagesToChat_toolResult_textRepresentation_splitsPerBlock(t *testing.T) {
	msgs := []RichMessage{
		{
			Role: "toolResult",
			Content: []ContentBlock{
				{Type: "text", Text: "result A", ToolCallID: "call_1", ToolName: "tool_a"},
				{Type: "text", Text: "result B", ToolCallID: "call_2", ToolName: "tool_b"},
			},
		},
	}

	got := RichMessagesToChat(msgs, "")

	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(got), got)
	}
	if got[0].Role != "tool" || got[0].Content != "result A" || got[0].ToolCallID != "call_1" {
		t.Errorf("message 0 = %+v, want role=tool content=result A tool_call_id=call_1", got[0])
	}
	if got[1].Role != "tool" || got[1].Content != "result B" || got[1].ToolCallID != "call_2" {
		t.Errorf("message 1 = %+v, want role=tool content=result B tool_call_id=call_2", got[1])
	}
}

// TestRichMessagesToChat_toolResult_dropsBlockWithoutToolCallID verifies that
// a block missing ToolCallID is dropped (strict providers reject "tool"
// messages without tool_call_id), while a sibling block in the same
// RichMessage that does have a ToolCallID still gets emitted.
func TestRichMessagesToChat_toolResult_dropsBlockWithoutToolCallID(t *testing.T) {
	msgs := []RichMessage{
		{
			Role: "toolResult",
			Content: []ContentBlock{
				{Type: "toolResult", Text: "orphan result", ToolCallID: ""},
				{Type: "toolResult", Text: "valid result", ToolCallID: "call_1"},
			},
		},
	}

	got := RichMessagesToChat(msgs, "")

	if len(got) != 1 {
		t.Fatalf("expected 1 message (orphan dropped), got %d: %+v", len(got), got)
	}
	if got[0].Role != "tool" || got[0].Content != "valid result" || got[0].ToolCallID != "call_1" {
		t.Errorf("message 0 = %+v, want role=tool content=valid result tool_call_id=call_1", got[0])
	}
}

// TestRichMessagesToChat_toolResult_singleBlock_unchangedShape is the
// production path: appendToolResults (agent/run_tools.go) always builds one
// RichMessage per tool call, with exactly one content block. That case must
// keep producing exactly one ChatMessage, unchanged in shape.
func TestRichMessagesToChat_toolResult_singleBlock_unchangedShape(t *testing.T) {
	msgs := []RichMessage{
		{
			Role: "toolResult",
			Content: []ContentBlock{
				{Type: "text", Text: "18C, cloudy", ToolCallID: "call_1", ToolName: "get_weather"},
			},
		},
	}

	got := RichMessagesToChat(msgs, "")

	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(got), got)
	}
	if got[0].Role != "tool" || got[0].Content != "18C, cloudy" || got[0].ToolCallID != "call_1" {
		t.Errorf("message 0 = %+v, want role=tool content=18C, cloudy tool_call_id=call_1", got[0])
	}
	if len(got[0].ToolCalls) != 0 {
		t.Errorf("message 0 ToolCalls = %+v, want none", got[0].ToolCalls)
	}
}
