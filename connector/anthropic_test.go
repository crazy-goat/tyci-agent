//go:build !noanthropic

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

// =============================================================================
// Request.Temperature wire-format tests
// =============================================================================

// TestAnthropicStream_Temperature verifies that Request.Temperature reaches
// the Anthropic wire body as a top-level "temperature" field, is completely
// absent when unset, and — crucially — is still present (with value 0) when
// the caller explicitly asked for deterministic sampling via a pointer to
// zero. Asserted on the actual marshaled JSON, not on the Go struct, so a
// regression in the json tag would be caught too.
func TestAnthropicStream_Temperature(t *testing.T) {
	ptr := func(v float64) *float64 { return &v }

	tests := []struct {
		name        string
		temperature *float64
		wantPresent bool
		wantValue   float64
	}{
		{name: "set", temperature: ptr(0.7), wantPresent: true, wantValue: 0.7},
		{name: "nil omits the key", temperature: nil, wantPresent: false},
		{name: "zero pointer still present", temperature: ptr(0), wantPresent: true, wantValue: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doer := &stubDoer{}
			c, err := NewAnthropic(Endpoint{
				BaseURL: "https://api.example.invalid",
				Path:    "/v1/messages",
				APIKey:  "sk-test",
				HTTP:    doer,
			})
			if err != nil {
				t.Fatalf("NewAnthropic: %v", err)
			}

			req := Request{Model: "claude-x", Temperature: tt.temperature}
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
