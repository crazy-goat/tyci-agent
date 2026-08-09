package connector

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/stream"
)

// =============================================================================
// messagesToChat tests
// =============================================================================

func TestMessagesToChat(t *testing.T) {
	tests := []struct {
		name   string
		msgs   []Message
		system string
		want   []api.ChatMessage
	}{
		{
			name:   "empty messages",
			msgs:   nil,
			system: "",
			want:   []api.ChatMessage{},
		},
		{
			name: "system message prepended",
			msgs: []Message{
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
			},
			system: "You are helpful.",
			want: []api.ChatMessage{
				{Role: "system", Content: "You are helpful."},
				{Role: "user", Content: "hello"},
			},
		},
		{
			name: "no system when empty",
			msgs: []Message{
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
			},
			system: "",
			want: []api.ChatMessage{
				{Role: "user", Content: "hello"},
			},
		},
		{
			name: "toolResult role maps to tool",
			msgs: []Message{
				{
					Role: "toolResult",
					Content: []ContentBlock{
						{Type: "text", Text: "result", ToolCallID: "call_123"},
					},
				},
			},
			want: []api.ChatMessage{
				{Role: "tool", Content: "result", ToolCallID: "call_123"},
			},
		},
		{
			name: "toolCall block",
			msgs: []Message{
				{
					Role: "assistant",
					Content: []ContentBlock{
						{
							Type:      "toolCall",
							ID:        "call_1",
							Name:      "bash",
							Arguments: json.RawMessage(`{"command":"ls"}`),
						},
					},
				},
			},
			want: []api.ChatMessage{
				{
					Role: "assistant",
					ToolCalls: []api.ChatToolCall{
						{
							ID:   "call_1",
							Type: "function",
							Function: api.ChatFunctionCall{
								Name:      "bash",
								Arguments: `{"command":"ls"}`,
							},
						},
					},
				},
			},
		},
		{
			name: "thinking blocks skipped",
			msgs: []Message{
				{
					Role: "assistant",
					Content: []ContentBlock{
						{Type: "thinking", Text: "internal thought"},
						{Type: "text", Text: "visible response"},
					},
				},
			},
			want: []api.ChatMessage{
				{Role: "assistant", Content: "visible response"},
			},
		},
		{
			name: "multiple text blocks concatenated",
			msgs: []Message{
				{
					Role: "user",
					Content: []ContentBlock{
						{Type: "text", Text: "hello "},
						{Type: "text", Text: "world"},
					},
				},
			},
			want: []api.ChatMessage{
				{Role: "user", Content: "hello world"},
			},
		},
		{
			name: "toolResult with isError",
			msgs: []Message{
				{
					Role: "toolResult",
					Content: []ContentBlock{
						{Type: "toolResult", Text: "error occurred", ToolCallID: "call_2", IsError: true},
					},
				},
			},
			want: []api.ChatMessage{
				{Role: "tool", Content: "error occurred", ToolCallID: "call_2"},
			},
		},
		{
			// Regression: a toolCall block without a function name must be
			// dropped — strict OpenAI-compatible providers (GLM, DeepSeek)
			// reject "tool_calls[0] is missing a function name" with 400.
			name: "toolCall with empty name is dropped",
			msgs: []Message{
				{
					Role: "assistant",
					Content: []ContentBlock{
						{Type: "toolCall", ID: "call_1", Name: "", Arguments: json.RawMessage(`{"command":"ls"}`)},
					},
				},
			},
			want: []api.ChatMessage{
				{Role: "assistant", Content: ""},
			},
		},
		{
			// Regression: a "tool" role message without tool_call_id must be
			// dropped — DeepSeek rejects "missing field `tool_call_id`".
			name: "tool message without tool_call_id is dropped",
			msgs: []Message{
				{
					Role: "toolResult",
					Content: []ContentBlock{
						{Type: "text", Text: "orphan result"},
					},
				},
			},
			want: []api.ChatMessage{},
		},
		{
			// Mixed: a well-formed toolCall is kept while a nameless one in
			// the same assistant message is dropped.
			name: "only malformed toolCalls dropped, valid kept",
			msgs: []Message{
				{
					Role: "assistant",
					Content: []ContentBlock{
						{Type: "toolCall", ID: "bad", Name: "", Arguments: json.RawMessage(`{}`)},
						{Type: "toolCall", ID: "good", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
					},
				},
			},
			want: []api.ChatMessage{
				{
					Role: "assistant",
					ToolCalls: []api.ChatToolCall{
						{ID: "good", Type: "function", Function: api.ChatFunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := messagesToChat(tt.msgs, tt.system)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d messages, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].Role != tt.want[i].Role {
					t.Errorf("[%d] Role = %q, want %q", i, got[i].Role, tt.want[i].Role)
				}
				if got[i].Content != tt.want[i].Content {
					t.Errorf("[%d] Content = %q, want %q", i, got[i].Content, tt.want[i].Content)
				}
				if got[i].ToolCallID != tt.want[i].ToolCallID {
					t.Errorf("[%d] ToolCallID = %q, want %q", i, got[i].ToolCallID, tt.want[i].ToolCallID)
				}
				if len(got[i].ToolCalls) != len(tt.want[i].ToolCalls) {
					t.Errorf("[%d] got %d ToolCalls, want %d", i, len(got[i].ToolCalls), len(tt.want[i].ToolCalls))
				} else {
					for j := range got[i].ToolCalls {
						if got[i].ToolCalls[j].ID != tt.want[i].ToolCalls[j].ID {
							t.Errorf("[%d][%d] ToolCall ID = %q, want %q", i, j, got[i].ToolCalls[j].ID, tt.want[i].ToolCalls[j].ID)
						}
						if got[i].ToolCalls[j].Function.Name != tt.want[i].ToolCalls[j].Function.Name {
							t.Errorf("[%d][%d] ToolCall Name = %q, want %q", i, j, got[i].ToolCalls[j].Function.Name, tt.want[i].ToolCalls[j].Function.Name)
						}
					}
				}
			}
		})
	}
}

// TestmessagesToChat_toolResult_splitsPerBlock covers the bug where a
// single toolResult Message carrying multiple tool-result blocks used to
// collapse into exactly one "tool" role ChatMessage (concatenated content,
// last tool_call_id wins). Each block must now produce its own message.
func TestMessagesToChat_toolResult_splitsPerBlock(t *testing.T) {
	msgs := []Message{
		{
			Role: "toolResult",
			Content: []ContentBlock{
				{Type: "toolResult", Text: "18C, cloudy", ToolCallID: "call_1", ToolName: "get_weather"},
				{Type: "toolResult", Text: "upstream weather service exploded", ToolCallID: "call_2", ToolName: "get_weather", IsError: true},
			},
		},
	}

	got := messagesToChat(msgs, "")

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

// TestmessagesToChat_toolResult_textRepresentation_splitsPerBlock covers
// the same fan-out, but for the "text" block shape with a non-empty
// ToolCallID — the representation agent/run_tools.go actually emits in
// production (see appendToolResults).
func TestMessagesToChat_toolResult_textRepresentation_splitsPerBlock(t *testing.T) {
	msgs := []Message{
		{
			Role: "toolResult",
			Content: []ContentBlock{
				{Type: "text", Text: "result A", ToolCallID: "call_1", ToolName: "tool_a"},
				{Type: "text", Text: "result B", ToolCallID: "call_2", ToolName: "tool_b"},
			},
		},
	}

	got := messagesToChat(msgs, "")

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

// TestmessagesToChat_toolResult_dropsBlockWithoutToolCallID verifies that
// a block missing ToolCallID is dropped (strict providers reject "tool"
// messages without tool_call_id), while a sibling block in the same
// Message that does have a ToolCallID still gets emitted.
func TestMessagesToChat_toolResult_dropsBlockWithoutToolCallID(t *testing.T) {
	msgs := []Message{
		{
			Role: "toolResult",
			Content: []ContentBlock{
				{Type: "toolResult", Text: "orphan result", ToolCallID: ""},
				{Type: "toolResult", Text: "valid result", ToolCallID: "call_1"},
			},
		},
	}

	got := messagesToChat(msgs, "")

	if len(got) != 1 {
		t.Fatalf("expected 1 message (orphan dropped), got %d: %+v", len(got), got)
	}
	if got[0].Role != "tool" || got[0].Content != "valid result" || got[0].ToolCallID != "call_1" {
		t.Errorf("message 0 = %+v, want role=tool content=valid result tool_call_id=call_1", got[0])
	}
}

// TestmessagesToChat_toolResult_singleBlock_unchangedShape is the
// production path: appendToolResults (agent/run_tools.go) always builds one
// Message per tool call, with exactly one content block. That case must
// keep producing exactly one ChatMessage, unchanged in shape.
func TestMessagesToChat_toolResult_singleBlock_unchangedShape(t *testing.T) {
	msgs := []Message{
		{
			Role: "toolResult",
			Content: []ContentBlock{
				{Type: "text", Text: "18C, cloudy", ToolCallID: "call_1", ToolName: "get_weather"},
			},
		},
	}

	got := messagesToChat(msgs, "")

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

// =============================================================================
// Request.Temperature wire-format tests
// =============================================================================

// TestOpenAIStream_Temperature verifies that Request.Temperature reaches the
// chat-completions wire body as a top-level "temperature" field, is
// completely absent when unset, and stays present (with value 0) for a
// pointer to zero — the "fully deterministic" case that a plain float64
// could not distinguish from "not set". Asserted on the actual marshaled
// JSON, not on the Go struct.
func TestOpenAIStream_Temperature(t *testing.T) {
	ptr := func(v float64) *float64 { return &v }

	tests := []struct {
		name        string
		temperature *float64
		wantPresent bool
		wantValue   float64
	}{
		{name: "set", temperature: ptr(1.3), wantPresent: true, wantValue: 1.3},
		{name: "nil omits the key", temperature: nil, wantPresent: false},
		{name: "zero pointer still present", temperature: ptr(0), wantPresent: true, wantValue: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doer := &stubDoer{}
			c, err := NewOpenAI(Endpoint{
				BaseURL: "https://api.example.invalid",
				Path:    "/v1/chat/completions",
				APIKey:  "sk-test",
				HTTP:    doer,
			})
			if err != nil {
				t.Fatalf("NewOpenAI: %v", err)
			}

			req := Request{Model: "gpt-4", Temperature: tt.temperature}
			if err := c.Stream(context.Background(), req, func(stream.Event) error { return nil }); err != nil {
				t.Fatalf("Stream: %v", err)
			}

			bodyBytes, err := io.ReadAll(doer.got.Body)
			if err != nil {
				t.Fatalf("reading captured request body: %v", err)
			}
			var payload map[string]any
			if err := json.Unmarshal(bodyBytes, &payload); err != nil {
				t.Fatalf("unmarshal request body: %v (body: %s)", err, bodyBytes)
			}

			temp, present := payload["temperature"]
			if present != tt.wantPresent {
				t.Fatalf("temperature key present = %v, want %v (body: %s)", present, tt.wantPresent, bodyBytes)
			}
			if tt.wantPresent {
				if got, ok := temp.(float64); !ok || got != tt.wantValue {
					t.Errorf("temperature = %v, want %v", temp, tt.wantValue)
				}
			}
		})
	}
}
