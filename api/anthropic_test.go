//go:build !noanthropic

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decodo/tyci/stream"
)

func TestConvertToolsToAnthropic_Null(t *testing.T) {
	got := ConvertToolsToAnthropic(nil)
	if string(got) != "null" && string(got) != "" {
		t.Errorf("expected nil/empty, got %s", string(got))
	}
}

func TestConvertToolsToAnthropic_EmptyArray(t *testing.T) {
	input := json.RawMessage(`[]`)
	got := ConvertToolsToAnthropic(input)
	if string(got) != "[]" {
		t.Errorf("expected [], got %s", string(got))
	}
}

func TestConvertToolsToAnthropic_AlreadyAnthropicFormat(t *testing.T) {
	// Anthropic format: [{"name":"x","input_schema":{}}] has no "function" key.
	// It should fail to parse as OpenAI and return as-is.
	input := json.RawMessage(`[{"name":"test","input_schema":{"type":"object"}}]`)
	got := ConvertToolsToAnthropic(input)
	if string(got) != string(input) {
		t.Errorf("expected pass-through, got %s", string(got))
	}
}

func TestConvertToolsToAnthropic_OpenAIFormat(t *testing.T) {
	input := json.RawMessage(`[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object"}}}]`)
	expected := `[{"description":"Get weather","input_schema":{"type":"object"},"name":"get_weather"}]`
	got := ConvertToolsToAnthropic(input)
	// Compare JSON equality
	var gotObj, expObj any
	if err := json.Unmarshal(got, &gotObj); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if err := json.Unmarshal([]byte(expected), &expObj); err != nil {
		t.Fatalf("failed to unmarshal expected: %v", err)
	}
	gotJSON, _ := json.Marshal(gotObj)
	expJSON, _ := json.Marshal(expObj)
	if string(gotJSON) != string(expJSON) {
		t.Errorf("got %s, want %s", string(gotJSON), string(expJSON))
	}
}

func TestConvertToolsToAnthropic_MultipleTools(t *testing.T) {
	input := json.RawMessage(`[
		{"type":"function","function":{"name":"a","parameters":{"type":"object"}}},
		{"type":"function","function":{"name":"b","description":"desc","parameters":{"type":"array"}}}
	]`)
	got := ConvertToolsToAnthropic(input)

	var tools []map[string]any
	if err := json.Unmarshal(got, &tools); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0]["name"] != "a" {
		t.Errorf("expected name 'a', got %v", tools[0]["name"])
	}
	if tools[1]["name"] != "b" {
		t.Errorf("expected name 'b', got %v", tools[1]["name"])
	}
	if tools[1]["description"] != "desc" {
		t.Errorf("expected description 'desc', got %v", tools[1]["description"])
	}
}

func TestConvertToolsToAnthropic_MissingFunctionField(t *testing.T) {
	// Objects without "function" key should be skipped
	input := json.RawMessage(`[{"type":"function","function":{"name":"a"}},{"type":"not_function"}]`)
	got := ConvertToolsToAnthropic(input)

	var tools []map[string]any
	if err := json.Unmarshal(got, &tools); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
}

func TestStreamAnthropic_TextOnly(t *testing.T) {
	sseEvents := `data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Hello"}}
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}
data: {"type":"content_block_stop","index":0}
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}
data: {"type":"message_stop"}
data: [DONE]
`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(sseEvents))
	}))
	defer server.Close()

	var events []stream.Event
	emit := func(e stream.Event) error {
		events = append(events, e)
		return nil
	}

	body := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		Stream:    true,
		Messages:  []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hi"}}}},
	}

	err := AnthropicStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err != nil {
		t.Fatalf("StreamAnthropic: %v", err)
	}

	// Expect: TextDelta("Hello"), TextDelta(" world"), Finish
	var texts []string
	var finish stream.Finish
	for _, e := range events {
		switch v := e.(type) {
		case stream.TextDelta:
			texts = append(texts, v.Text)
		case stream.Finish:
			finish = v
		}
	}

	if len(texts) != 2 || texts[0] != "Hello" || texts[1] != " world" {
		t.Errorf("expected text deltas ['Hello', ' world'], got %v", texts)
	}
	if finish.Reason != "end_turn" {
		t.Errorf("expected reason 'end_turn', got %q", finish.Reason)
	}
	if finish.Usage.Input != 10 {
		t.Errorf("expected input tokens 10, got %d", finish.Usage.Input)
	}
	if finish.Usage.Output != 5 {
		t.Errorf("expected output tokens 5, got %d", finish.Usage.Output)
	}
}

func TestStreamAnthropic_ToolUse(t *testing.T) {
	sseEvents := `data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"get_weather","input":{}}}
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"location\":"}}
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"London\"}"}}
data: {"type":"content_block_stop","index":0}
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}
data: [DONE]
`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(sseEvents))
	}))
	defer server.Close()

	var events []stream.Event
	emit := func(e stream.Event) error {
		events = append(events, e)
		return nil
	}

	body := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		Stream:    true,
		Messages:  []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "weather?"}}}},
	}

	err := AnthropicStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err != nil {
		t.Fatalf("StreamAnthropic: %v", err)
	}

	var toolCalls []stream.ToolCall
	for _, e := range events {
		switch v := e.(type) {
		case stream.ToolCall:
			toolCalls = append(toolCalls, v)
		}
	}

	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].ID != "tu_1" {
		t.Errorf("expected ID 'tu_1', got %q", toolCalls[0].ID)
	}
	if toolCalls[0].Name != "get_weather" {
		t.Errorf("expected Name 'get_weather', got %q", toolCalls[0].Name)
	}
	if !strings.Contains(toolCalls[0].Arguments, "London") {
		t.Errorf("expected arguments to contain London, got %q", toolCalls[0].Arguments)
	}
}

func TestStreamAnthropic_EmptyStream(t *testing.T) {
	// Only [DONE] — should emit Finish with default "stop" reason
	sseEvents := "data: [DONE]\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(sseEvents))
	}))
	defer server.Close()

	var events []stream.Event
	emit := func(e stream.Event) error {
		events = append(events, e)
		return nil
	}

	body := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		Stream:    true,
		Messages:  []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hi"}}}},
	}

	err := AnthropicStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err != nil {
		t.Fatalf("StreamAnthropic: %v", err)
	}

	var finish *stream.Finish
	for _, e := range events {
		if f, ok := e.(stream.Finish); ok {
			finish = &f
			break
		}
	}
	if finish == nil {
		t.Fatal("expected Finish event")
	}
	if finish.Reason != "stop" {
		t.Errorf("expected reason 'stop', got %q", finish.Reason)
	}
}

// =============================================================================
// AnthropicRequest.Temperature wire-format tests
// =============================================================================

// TestAnthropicRequest_Temperature_Marshaling mirrors
// TestChatRequest_Temperature_Marshaling for the Anthropic request shape:
// set -> top-level "temperature"; nil -> key absent; zero pointer -> key
// present with value 0.
func TestAnthropicRequest_Temperature_Marshaling(t *testing.T) {
	ptr := func(v float64) *float64 { return &v }

	tests := []struct {
		name        string
		temperature *float64
		wantPresent bool
		wantValue   float64
	}{
		{name: "set", temperature: ptr(0.4), wantPresent: true, wantValue: 0.4},
		{name: "nil omits the key", temperature: nil, wantPresent: false},
		{name: "zero pointer still present", temperature: ptr(0), wantPresent: true, wantValue: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := AnthropicRequest{Model: "claude-x", MaxTokens: 100, Temperature: tt.temperature}
			data, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var payload map[string]any
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("Unmarshal: %v (data: %s)", err, data)
			}
			temp, present := payload["temperature"]
			if present != tt.wantPresent {
				t.Fatalf("temperature key present = %v, want %v (data: %s)", present, tt.wantPresent, data)
			}
			if tt.wantPresent {
				if got, ok := temp.(float64); !ok || got != tt.wantValue {
					t.Errorf("temperature = %v, want %v", temp, tt.wantValue)
				}
			}
		})
	}
}

func TestStreamAnthropic_ErrorStatusCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	emit := func(e stream.Event) error { return nil }
	body := AnthropicRequest{Model: "claude-sonnet-4-20250514", MaxTokens: 100, Stream: true}

	err := AnthropicStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err == nil {
		t.Fatal("expected error for 429")
	}
	var re *RetryableError
	if !errors.As(err, &re) {
		t.Fatalf("expected RetryableError, got %T: %v", err, err)
	}
	if re.Code != 429 {
		t.Errorf("expected code 429, got %d", re.Code)
	}
}
