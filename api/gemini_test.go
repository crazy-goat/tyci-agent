//go:build !nogemini

package api

import (
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
