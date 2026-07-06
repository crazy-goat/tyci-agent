package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/decodo/tyci/api"
)

// =============================================================================
// RichMessagesToChat tests
// =============================================================================

func TestRichMessagesToChat(t *testing.T) {
	tests := []struct {
		name   string
		msgs   []RichMessage
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
			},
			system: "",
			want: []api.ChatMessage{
				{Role: "user", Content: "hello"},
			},
		},
		{
			name: "toolResult role maps to tool",
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			got := RichMessagesToChat(tt.msgs, tt.system)
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

// =============================================================================
// RichMessagesToAnthropic tests
// =============================================================================

func TestRichMessagesToAnthropic(t *testing.T) {
	tests := []struct {
		name string
		msgs []RichMessage
		want []api.AnthropicMessage
	}{
		{
			name: "empty",
			msgs: nil,
			want: []api.AnthropicMessage{},
		},
		{
			name: "user text message",
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			got := RichMessagesToAnthropic(tt.msgs)
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
// RichMessagesToGemini tests
// =============================================================================

func TestRichMessagesToGemini(t *testing.T) {
	tests := []struct {
		name       string
		msgs       []RichMessage
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
			msgs: []RichMessage{
				{Role: "system", Content: []ContentBlock{{Type: "text", Text: "You are helpful."}}},
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
			},
			wantParts:  1,
			wantSystem: "You are helpful.",
		},
		{
			name: "user text",
			msgs: []RichMessage{
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
			},
			wantParts:  1,
			wantSystem: "",
		},
		{
			name: "toolResult maps to function role",
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			msgs: []RichMessage{
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
			contents, systemText := RichMessagesToGemini(tt.msgs)
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
// LoadConfig tests
// =============================================================================

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string][]ModelEntry
		wantErr bool
	}{
		{
			name:    "file not found",
			content: "",
			want:    nil,
			wantErr: false,
		},
		{
			name: "new format",
			content: `{
				"openai": {
					"gpt-4": {"uri": "openai://gpt-4@sk-test@api.openai.com"},
					"gpt-3.5": {"uri": "openai://gpt-3.5-turbo@api.openai.com"}
				}
			}`,
			want: map[string][]ModelEntry{
				"openai": {
					{Name: "gpt-4", URI: "openai://gpt-4@sk-test@api.openai.com"},
					{Name: "gpt-3.5", URI: "openai://gpt-3.5-turbo@api.openai.com"},
				},
			},
		},
		{
			name: "legacy format with providers wrapper",
			content: `{
				"providers": {
					"anthropic": [
						{"uri": "anthropic://claude-3@sk-test@api.anthropic.com"}
					]
				}
			}`,
			want: map[string][]ModelEntry{
				"anthropic": {
					{Name: "claude-3", URI: "anthropic://claude-3@sk-test@api.anthropic.com"},
				},
			},
		},
		{
			name: "legacy format without wrapper",
			content: `{
				"openai": [
					{"uri": "openai://gpt-4@sk-test@api.openai.com"}
				]
			}`,
			want: map[string][]ModelEntry{
				"openai": {
					{Name: "gpt-4", URI: "openai://gpt-4@sk-test@api.openai.com"},
				},
			},
		},
		{
			name:    "invalid JSON",
			content: `{invalid json`,
			wantErr: true,
		},
		{
			name:    "empty object",
			content: `{}`,
			want:    map[string][]ModelEntry{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var path string
			if tt.content == "" {
				// Test file not found
				path = filepath.Join(t.TempDir(), "nonexistent.json")
			} else {
				path = filepath.Join(t.TempDir(), "model.json")
				if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			got, err := LoadConfig(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("LoadConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if len(got) != len(tt.want) {
				t.Fatalf("got %d providers, want %d", len(got), len(tt.want))
			}
			// Collect all entries across providers and compare as sets (map iteration is non-deterministic)
			var gotAll, wantAll []ModelEntry
			for _, entries := range got {
				gotAll = append(gotAll, entries...)
			}
			for _, entries := range tt.want {
				wantAll = append(wantAll, entries...)
			}
			if len(gotAll) != len(wantAll) {
				t.Fatalf("got %d total entries, want %d", len(gotAll), len(wantAll))
			}
			// Sort both for comparison
			sort.Slice(gotAll, func(i, j int) bool { return gotAll[i].Name < gotAll[j].Name })
			sort.Slice(wantAll, func(i, j int) bool { return wantAll[i].Name < wantAll[j].Name })
			for i := range gotAll {
				if gotAll[i].Name != wantAll[i].Name {
					t.Errorf("[%d] Name = %q, want %q", i, gotAll[i].Name, wantAll[i].Name)
				}
				if gotAll[i].URI != wantAll[i].URI {
					t.Errorf("[%d] URI = %q, want %q", i, gotAll[i].URI, wantAll[i].URI)
				}
			}
		})
	}
}

// =============================================================================
// MustLoadConfig tests
// =============================================================================

func TestMustLoadConfig(t *testing.T) {
	// Non-existent file should return empty map, not panic
	got := MustLoadConfig(filepath.Join(t.TempDir(), "nonexistent.json"))
	if got == nil {
		t.Fatal("MustLoadConfig returned nil, want empty map")
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}

	// Valid file
	path := filepath.Join(t.TempDir(), "model.json")
	content := `{"openai": {"gpt-4": {"uri": "openai://gpt-4@api.openai.com"}}}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	got = MustLoadConfig(path)
	if len(got) != 1 {
		t.Errorf("got %d providers, want 1", len(got))
	}
}

// =============================================================================
// parseURI additional tests
// =============================================================================

func TestParseURI_table(t *testing.T) {
	tests := []struct {
		name        string
		uri         string
		wantAPIType string
		wantToken   string
		wantHost    string
		wantPath    string
		wantErr     bool
	}{
		{
			name:        "openai without token",
			uri:         "openai://gpt-4@api.openai.com",
			wantAPIType: "openai",
			wantToken:   "",
			wantHost:    "https://api.openai.com",
			wantPath:    "/v1/chat/completions",
		},
		{
			name:        "openai with token",
			uri:         "openai://gpt-4@sk-test@api.openai.com",
			wantAPIType: "openai",
			wantToken:   "sk-test",
			wantHost:    "https://api.openai.com",
			wantPath:    "/v1/chat/completions",
		},
		{
			name:        "anthropic without token",
			uri:         "anthropic://claude-sonnet-4@api.anthropic.com",
			wantAPIType: "anthropic",
			wantToken:   "",
			wantHost:    "https://api.anthropic.com",
			wantPath:    "/v1/messages",
		},
		{
			name:        "anthropic with token",
			uri:         "anthropic://claude-sonnet-4@sk-ant-test@api.anthropic.com",
			wantAPIType: "anthropic",
			wantToken:   "sk-ant-test",
			wantHost:    "https://api.anthropic.com",
			wantPath:    "/v1/messages",
		},
		{
			name:        "gemini",
			uri:         "gemini://gemini-2.0-flash@generativelanguage.googleapis.com",
			wantAPIType: "gemini",
			wantToken:   "",
			wantHost:    "https://generativelanguage.googleapis.com",
			wantPath:    "",
		},
		{
			name:        "unknown api type defaults to openai",
			uri:         "custom://model@api.custom.com",
			wantAPIType: "openai",
			wantToken:   "",
			wantHost:    "https://api.custom.com",
			wantPath:    "/v1/chat/completions",
		},
		{
			name:    "invalid - no scheme",
			uri:     "gpt-4@api.openai.com",
			wantErr: true,
		},
		{
			name:    "invalid - empty string",
			uri:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiType, token, baseURL, endpointPath, err := parseURI(tt.uri)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseURI() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if apiType != tt.wantAPIType {
				t.Errorf("apiType = %q, want %q", apiType, tt.wantAPIType)
			}
			if token != tt.wantToken {
				t.Errorf("token = %q, want %q", token, tt.wantToken)
			}
			if baseURL != tt.wantHost {
				t.Errorf("baseURL = %q, want %q", baseURL, tt.wantHost)
			}
			if endpointPath != tt.wantPath {
				t.Errorf("endpointPath = %q, want %q", endpointPath, tt.wantPath)
			}
		})
	}
}

// =============================================================================
// parseModel tests
// =============================================================================

func TestParseModel(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{"openai model", "openai://gpt-4@api.openai.com", "gpt-4"},
		{"anthropic model", "anthropic://claude-3@api.anthropic.com", "claude-3"},
		{"with token", "openai://gpt-4@sk-test@api.openai.com", "gpt-4"},
		{"invalid uri", "not-a-uri", ""},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseModel(tt.uri)
			if got != tt.want {
				t.Errorf("parseModel(%q) = %q, want %q", tt.uri, got, tt.want)
			}
		})
	}
}

// =============================================================================
// convertToolsToGemini tests
// =============================================================================

func TestConvertToolsToGemini(t *testing.T) {
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
			got := convertToolsToGemini(json.RawMessage(tt.in))
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
