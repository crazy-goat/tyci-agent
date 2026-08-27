package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/decodo/tyci/stream"
)

func TestMessagesToResponses(t *testing.T) {
	args := json.RawMessage(`{"path":"README.md"}`)
	got := messagesToResponses([]Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "read this"}}},
		{Role: "assistant", Content: []ContentBlock{
			{Type: "thinking", Thinking: "internal"},
			{Type: "toolCall", ID: "call_1", Name: "read", Arguments: args},
		}},
		{Role: "toolResult", Content: []ContentBlock{{Type: "text", Text: "contents", ToolCallID: "call_1"}}},
	})

	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `[{
		"role":"user",
		"content":[{"type":"input_text","text":"read this"}]
	},{
		"type":"function_call",
		"call_id":"call_1",
		"name":"read",
		"arguments":"{\"path\":\"README.md\"}",
		"status":"completed"
	},{
		"type":"function_call_output",
		"call_id":"call_1",
		"output":"contents"
	}]`
	var gotJSON, wantJSON any
	if err := json.Unmarshal(data, &gotJSON); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantJSON); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	gotBytes, _ := json.Marshal(gotJSON)
	wantBytes, _ := json.Marshal(wantJSON)
	if string(gotBytes) != string(wantBytes) {
		t.Fatalf("messages JSON = %s, want %s", gotBytes, wantBytes)
	}
}

func TestConvertToolsToResponses(t *testing.T) {
	input := json.RawMessage(`[{"type":"function","function":{"name":"read","description":"Read a file","parameters":{"type":"object"},"strict":false}}]`)
	got := convertToolsToResponses(input)
	want := `[{
		"type":"function",
		"name":"read",
		"description":"Read a file",
		"parameters":{"type":"object"},
		"strict":false
	}]`
	var gotJSON, wantJSON any
	if err := json.Unmarshal(got, &gotJSON); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantJSON); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	gotBytes, _ := json.Marshal(gotJSON)
	wantBytes, _ := json.Marshal(wantJSON)
	if string(gotBytes) != string(wantBytes) {
		t.Fatalf("tools JSON = %s, want %s", gotBytes, wantBytes)
	}
}

type responsesRequestDoer struct {
	request *http.Request
}

func (d *responsesRequestDoer) Do(req *http.Request) (*http.Response, error) {
	d.request = req
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n",
		)),
		Request: req,
	}, nil
}

func TestResponsesStreamRequest(t *testing.T) {
	doer := &responsesRequestDoer{}
	c, err := NewResponses(Endpoint{
		BaseURL: "https://api.example.invalid",
		Path:    "/v1/responses",
		APIKey:  "sk-test",
		HTTP:    doer,
		Options: map[string]string{OptReasoningEffort: "xhigh"},
	})
	if err != nil {
		t.Fatalf("NewResponses: %v", err)
	}

	err = c.Stream(context.Background(), Request{
		Model:  "gpt-5.6-luna",
		System: "Be concise.",
		Messages: []Message{{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: "hello"}},
		}},
		Tools:     json.RawMessage(`[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]`),
		MaxTokens: 321,
	}, func(stream.Event) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if doer.request.URL.String() != "https://api.example.invalid/v1/responses" {
		t.Fatalf("URL = %q", doer.request.URL)
	}
	if got := doer.request.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", got)
	}
	body, err := io.ReadAll(doer.request.Body)
	if err != nil {
		t.Fatalf("Read request body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("request JSON: %v (body %s)", err, body)
	}
	if payload["model"] != "gpt-5.6-luna" || payload["instructions"] != "Be concise." {
		t.Fatalf("request identity fields = %#v", payload)
	}
	if payload["max_output_tokens"] != float64(321) {
		t.Fatalf("max_output_tokens = %#v", payload["max_output_tokens"])
	}
	if payload["stream"] != true {
		t.Fatalf("stream = %#v", payload["stream"])
	}
	reasoning, ok := payload["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "xhigh" {
		t.Fatalf("reasoning = %#v", payload["reasoning"])
	}
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", payload["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "read" || tool["function"] != nil {
		t.Fatalf("Responses tool shape = %#v", tool)
	}
}

// TestResponsesStream_FallbacksQueryParam covers the Responses API path for
// TODO.md item 50: ?fallbacks=... must reach the outgoing request as a URL
// query parameter, merged with ?reasoning=... (a body field, sent
// separately) without either being dropped or duplicated.
func TestResponsesStream_FallbacksQueryParam(t *testing.T) {
	doer := &responsesRequestDoer{}
	c, err := NewResponses(Endpoint{
		BaseURL: "https://api.nexos.ai",
		Path:    "/v1/responses",
		APIKey:  "sk-test",
		HTTP:    doer,
		Options: map[string]string{OptReasoningEffort: "xhigh", OptFallbacks: "false"},
	})
	if err != nil {
		t.Fatalf("NewResponses: %v", err)
	}

	err = c.Stream(context.Background(), Request{
		Model: "gpt-5.6-luna",
		Messages: []Message{{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: "hello"}},
		}},
	}, func(stream.Event) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	wantURL := "https://api.nexos.ai/v1/responses?fallbacks=false"
	if got := doer.request.URL.String(); got != wantURL {
		t.Fatalf("request URL = %q, want %q", got, wantURL)
	}

	body, err := io.ReadAll(doer.request.Body)
	if err != nil {
		t.Fatalf("Read request body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("request JSON: %v (body %s)", err, body)
	}
	reasoning, ok := payload["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "xhigh" {
		t.Fatalf("reasoning effort still reaches the body alongside the query fallbacks option: got %#v", payload["reasoning"])
	}
}
