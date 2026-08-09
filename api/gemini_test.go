//go:build !nogemini

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/decodo/tyci/stream"
)

// Test StreamGemini
func TestStreamGemini_TextResponse(t *testing.T) {
	sseEvents := `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}
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

	body := GeminiRequest{
		Contents: []GeminiContent{{Parts: []GeminiPart{{Text: "hi"}}}},
		Stream:   true,
	}

	err := GeminiStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err != nil {
		t.Fatalf("StreamGemini: %v", err)
	}

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

	if len(texts) != 1 || texts[0] != "Hello" {
		t.Errorf("expected text 'Hello', got %v", texts)
	}
	if finish.Usage.Input != 10 {
		t.Errorf("expected input 10, got %d", finish.Usage.Input)
	}
	if finish.Usage.Output != 5 {
		t.Errorf("expected output 5, got %d", finish.Usage.Output)
	}
}

func TestStreamGemini_ToolCalls(t *testing.T) {
	sseEvents := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"location":"NY"}}}]},"finishReason":"STOP"}]}
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

	body := GeminiRequest{
		Contents: []GeminiContent{{Parts: []GeminiPart{{Text: "weather?"}}}},
		Stream:   true,
	}

	err := GeminiStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err != nil {
		t.Fatalf("StreamGemini: %v", err)
	}

	var toolCalls []stream.ToolCall
	for _, e := range events {
		if tc, ok := e.(stream.ToolCall); ok {
			toolCalls = append(toolCalls, tc)
		}
	}

	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Name != "get_weather" {
		t.Errorf("expected name 'get_weather', got %q", toolCalls[0].Name)
	}
}

func TestStreamGemini_EmptyStream(t *testing.T) {
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

	body := GeminiRequest{
		Contents: []GeminiContent{{Parts: []GeminiPart{{Text: "hi"}}}},
		Stream:   true,
	}

	err := GeminiStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err != nil {
		t.Fatalf("StreamGemini: %v", err)
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

func TestStreamGemini_Error429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	emit := func(e stream.Event) error { return nil }
	body := GeminiRequest{Contents: []GeminiContent{{Parts: []GeminiPart{{Text: "hi"}}}}, Stream: true}

	err := GeminiStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err == nil {
		t.Fatal("expected error for 429")
	}
	var re *RetryableError
	if !as(err, &re) {
		t.Fatalf("expected RetryableError, got %T: %v", err, err)
	}
	if re.Code != 429 {
		t.Errorf("expected code 429, got %d", re.Code)
	}
}

func TestStreamGemini_Error500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("server error"))
	}))
	defer server.Close()

	emit := func(e stream.Event) error { return nil }
	body := GeminiRequest{Contents: []GeminiContent{{Parts: []GeminiPart{{Text: "hi"}}}}, Stream: true}

	err := GeminiStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err == nil {
		t.Fatal("expected error for 500")
	}
	var re *RetryableError
	if !as(err, &re) {
		t.Fatalf("expected RetryableError, got %T: %v", err, err)
	}
	if re.Code != 500 {
		t.Errorf("expected code 500, got %d", re.Code)
	}
}

// =============================================================================
// GeminiRequest.GenerationConfig.Temperature wire-format tests
// =============================================================================

// TestGeminiRequest_Temperature_Marshaling verifies Temperature lands under
// the nested "generationConfig" object rather than top-level — the one
// respect in which Gemini's request shape differs from Anthropic's and
// OpenAI's. When GenerationConfig is nil (the connector's job is to leave
// it nil unless Temperature was set), "generationConfig" must not appear
// in the JSON at all, not even as "{}" — the golden-file guarantee this
// layer must not break for existing (temperature-less) requests.
func TestGeminiRequest_Temperature_Marshaling(t *testing.T) {
	ptr := func(v float64) *float64 { return &v }

	tests := []struct {
		name            string
		genConfig       *GeminiGenerationConfig
		wantConfigKey   bool
		wantTempPresent bool
		wantValue       float64
	}{
		{name: "set", genConfig: &GeminiGenerationConfig{Temperature: ptr(1.1)}, wantConfigKey: true, wantTempPresent: true, wantValue: 1.1},
		{name: "nil GenerationConfig omits the key entirely", genConfig: nil, wantConfigKey: false},
		{name: "zero pointer still present", genConfig: &GeminiGenerationConfig{Temperature: ptr(0)}, wantConfigKey: true, wantTempPresent: true, wantValue: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := GeminiRequest{
				Contents:         []GeminiContent{{Parts: []GeminiPart{{Text: "hi"}}}},
				GenerationConfig: tt.genConfig,
			}
			data, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var payload map[string]any
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("Unmarshal: %v (data: %s)", err, data)
			}
			genConfig, present := payload["generationConfig"]
			if present != tt.wantConfigKey {
				t.Fatalf("generationConfig key present = %v, want %v (data: %s)", present, tt.wantConfigKey, data)
			}
			if !tt.wantConfigKey {
				return
			}
			configMap, ok := genConfig.(map[string]any)
			if !ok {
				t.Fatalf("generationConfig is not an object: %T (%v)", genConfig, genConfig)
			}
			temp, tempPresent := configMap["temperature"]
			if tempPresent != tt.wantTempPresent {
				t.Fatalf("generationConfig.temperature present = %v, want %v (data: %s)", tempPresent, tt.wantTempPresent, data)
			}
			if tt.wantTempPresent {
				if got, ok := temp.(float64); !ok || got != tt.wantValue {
					t.Errorf("generationConfig.temperature = %v, want %v", temp, tt.wantValue)
				}
			}
		})
	}
}

func TestStreamGemini_Error400(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer server.Close()

	emit := func(e stream.Event) error { return nil }
	body := GeminiRequest{Contents: []GeminiContent{{Parts: []GeminiPart{{Text: "hi"}}}}, Stream: true}

	err := GeminiStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	// 400 is not retryable
	var re *RetryableError
	if as(err, &re) {
		t.Error("400 should not be RetryableError")
	}
}
