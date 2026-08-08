package connector

import (
	"encoding/json"
	"testing"

	"github.com/decodo/tyci/api"
)

// =============================================================================
// messagesToAnthropic tests
// =============================================================================

func TestMessagesToAnthropic(t *testing.T) {
	tests := []struct {
		name string
		msgs []Message
		want []api.AnthropicMessage
	}{
		{
			name: "empty",
			msgs: nil,
			want: []api.AnthropicMessage{},
		},
		{
			name: "user text message",
			msgs: []Message{
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
			},
			want: []api.AnthropicMessage{
				{
					Role: "user",
					Content: []api.AnthropicContentBlock{
						{Type: "text", Text: "hello"},
					},
				},
			},
		},
		{
			name: "toolResult maps to user role",
			msgs: []Message{
				{
					Role: "toolResult",
					Content: []ContentBlock{
						{Type: "text", Text: "result data", ToolCallID: "call_123"},
					},
				},
			},
			want: []api.AnthropicMessage{
				{
					Role: "user",
					Content: []api.AnthropicContentBlock{
						{
							Type:      "tool_result",
							ToolUseID: "call_123",
							Content: []struct {
								Type string `json:"type"`
								Text string `json:"text"`
							}{{Type: "text", Text: "result data"}},
						},
					},
				},
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
							ID:        "toolu_1",
							Name:      "bash",
							Arguments: json.RawMessage(`{"command":"pwd"}`),
						},
					},
				},
			},
			want: []api.AnthropicMessage{
				{
					Role: "assistant",
					Content: []api.AnthropicContentBlock{
						{
							Type:  "tool_use",
							ID:    "toolu_1",
							Name:  "bash",
							Input: map[string]any{"command": "pwd"},
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
						{Type: "thinking", Text: "thinking content"},
						{Type: "text", Text: "visible content"},
					},
				},
			},
			want: []api.AnthropicMessage{
				{
					Role: "assistant",
					Content: []api.AnthropicContentBlock{
						{Type: "text", Text: "visible content"},
					},
				},
			},
		},
		{
			name: "toolResult type block with isError",
			msgs: []Message{
				{
					Role: "toolResult",
					Content: []ContentBlock{
						{Type: "toolResult", Text: "error", ToolCallID: "call_2", IsError: true},
					},
				},
			},
			want: []api.AnthropicMessage{
				{
					Role: "user",
					Content: []api.AnthropicContentBlock{
						{
							Type:      "tool_result",
							ToolUseID: "call_2",
							Content: []struct {
								Type string `json:"type"`
								Text string `json:"text"`
							}{{Type: "text", Text: "error"}},
							IsError: true,
						},
					},
				},
			},
		},
		{
			name: "toolCall with nil arguments",
			msgs: []Message{
				{
					Role: "assistant",
					Content: []ContentBlock{
						{Type: "toolCall", ID: "toolu_2", Name: "read"},
					},
				},
			},
			want: []api.AnthropicMessage{
				{
					Role: "assistant",
					Content: []api.AnthropicContentBlock{
						{Type: "tool_use", ID: "toolu_2", Name: "read", Input: nil},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := messagesToAnthropic(tt.msgs)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d messages, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].Role != tt.want[i].Role {
					t.Errorf("[%d] Role = %q, want %q", i, got[i].Role, tt.want[i].Role)
				}
				if len(got[i].Content) != len(tt.want[i].Content) {
					t.Errorf("[%d] got %d content blocks, want %d", i, len(got[i].Content), len(tt.want[i].Content))
					continue
				}
				for j := range got[i].Content {
					if got[i].Content[j].Type != tt.want[i].Content[j].Type {
						t.Errorf("[%d][%d] Type = %q, want %q", i, j, got[i].Content[j].Type, tt.want[i].Content[j].Type)
					}
					if got[i].Content[j].Text != tt.want[i].Content[j].Text {
						t.Errorf("[%d][%d] Text = %q, want %q", i, j, got[i].Content[j].Text, tt.want[i].Content[j].Text)
					}
				}
			}
		})
	}
}
