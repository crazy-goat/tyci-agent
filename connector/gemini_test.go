package connector

import (
	"encoding/json"
	"testing"
)

// =============================================================================
// messagesToGemini tests
// =============================================================================

func TestMessagesToGemini(t *testing.T) {
	tests := []struct {
		name       string
		msgs       []Message
		wantParts  int
		wantSystem string
	}{
		{
			name:       "empty",
			msgs:       nil,
			wantParts:  0,
			wantSystem: "",
		},
		{
			name: "system message extracts systemText",
			msgs: []Message{
				{Role: "system", Content: []ContentBlock{{Type: "text", Text: "You are helpful."}}},
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
			},
			wantParts:  1,
			wantSystem: "You are helpful.",
		},
		{
			name: "user text",
			msgs: []Message{
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
			},
			wantParts:  1,
			wantSystem: "",
		},
		{
			name: "toolResult maps to function role",
			msgs: []Message{
				{
					Role: "toolResult",
					Content: []ContentBlock{
						{Type: "text", Text: "output", ToolCallID: "call_1", ToolName: "bash"},
					},
				},
			},
			wantParts: 1,
		},
		{
			name: "toolCall becomes functionCall",
			msgs: []Message{
				{
					Role: "assistant",
					Content: []ContentBlock{
						{
							Type:      "toolCall",
							Name:      "bash",
							Arguments: json.RawMessage(`{"command":"ls"}`),
						},
					},
				},
			},
			wantParts: 1,
		},
		{
			name: "thinking blocks skipped",
			msgs: []Message{
				{
					Role: "assistant",
					Content: []ContentBlock{
						{Type: "thinking", Text: "thought"},
						{Type: "text", Text: "response"},
					},
				},
			},
			wantParts: 1,
		},
		{
			name: "toolCall with nil arguments uses empty object",
			msgs: []Message{
				{
					Role: "assistant",
					Content: []ContentBlock{
						{Type: "toolCall", Name: "read"},
					},
				},
			},
			wantParts: 1,
		},
		{
			name: "toolResult type block",
			msgs: []Message{
				{
					Role: "function",
					Content: []ContentBlock{
						{Type: "toolResult", Text: "file content", ToolName: "read"},
					},
				},
			},
			wantParts: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contents, systemText := messagesToGemini(tt.msgs)
			if systemText != tt.wantSystem {
				t.Errorf("systemText = %q, want %q", systemText, tt.wantSystem)
			}
			totalParts := 0
			for _, c := range contents {
				totalParts += len(c.Parts)
			}
			if totalParts != tt.wantParts {
				t.Errorf("got %d parts, want %d", totalParts, tt.wantParts)
			}
		})
	}
}

// =============================================================================
// toolsToGemini tests
// =============================================================================

func TestToolsToGemini(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int // number of function declarations
	}{
		{
			name: "empty",
			in:   `[]`,
			want: 0,
		},
		{
			name: "single tool",
			in:   `[{"type":"function","function":{"name":"bash","description":"Run bash"}}]`,
			want: 1,
		},
		{
			name: "multiple tools",
			in: `[{"type":"function","function":{"name":"bash","description":"Run bash"}},
			     {"type":"function","function":{"name":"read","description":"Read file"}}]`,
			want: 2,
		},
		{
			name: "invalid JSON",
			in:   `invalid`,
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolsToGemini(json.RawMessage(tt.in))
			totalDecls := 0
			for _, g := range got {
				totalDecls += len(g.FunctionDeclarations)
			}
			if totalDecls != tt.want {
				t.Errorf("got %d function declarations, want %d", totalDecls, tt.want)
			}
		})
	}
}
