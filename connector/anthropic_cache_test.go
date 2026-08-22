//go:build !noanthropic

package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/decodo/tyci/stream"
)

// capturingDoer keeps the request body so a test can assert on the JSON that
// actually went out, rather than on the struct that was built to produce it.
type capturingDoer struct {
	body []byte
}

func (d *capturingDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		d.body, _ = io.ReadAll(req.Body)
	}
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n")),
		Request:    req,
	}, nil
}

// anthropicRequestBody runs one request through the Anthropic connector and
// returns the JSON body it sent, decoded.
func anthropicRequestBody(t *testing.T, req Request) map[string]any {
	t.Helper()

	doer := &capturingDoer{}
	c, err := NewAnthropic(Endpoint{
		BaseURL: "https://api.example.invalid",
		Path:    "/v1/messages",
		APIKey:  "sk-test",
		HTTP:    doer,
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	if err := c.Stream(context.Background(), req, func(stream.Event) error { return nil }); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(doer.body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, doer.body)
	}
	return body
}

func cacheRequest() Request {
	return Request{
		Model:  "claude-sonnet-5",
		System: "you are a coding agent",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "read main.go"}}},
		},
		Tools: json.RawMessage(`[{"type":"function","function":{"name":"read","description":"read a file","parameters":{"type":"object"}}}]`),
	}
}

// TestPromptCacheIsOnByDefault is the whole point: without a cache_control
// breakpoint, every turn re-sends and re-pays for the tool schemas, the system
// prompt and the entire conversation so far.
func TestPromptCacheIsOnByDefault(t *testing.T) {
	body := anthropicRequestBody(t, cacheRequest())

	// Tools: the breakpoint belongs on the last one, closing the schema block.
	tools, _ := body["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools in the request")
	}
	last, _ := tools[len(tools)-1].(map[string]any)
	if last["cache_control"] == nil {
		t.Error("the last tool has no cache_control, so the schemas are re-processed every turn")
	}

	// System: only the list form can carry cache_control at all.
	system, ok := body["system"].([]any)
	if !ok {
		t.Fatalf("system should be a list of blocks, got %T", body["system"])
	}
	sysBlock, _ := system[len(system)-1].(map[string]any)
	if sysBlock["cache_control"] == nil {
		t.Error("the system prompt is not marked cacheable")
	}
	if sysBlock["text"] != "you are a coding agent" {
		t.Errorf("system text did not survive the block conversion: %v", sysBlock["text"])
	}

	// Messages: the breakpoint has to sit on the LAST block of the LAST
	// message — that is the prefix the next turn re-sends verbatim.
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	lastMsg, _ := msgs[len(msgs)-1].(map[string]any)
	blocks, _ := lastMsg["content"].([]any)
	lastBlock, _ := blocks[len(blocks)-1].(map[string]any)
	if lastBlock["cache_control"] == nil {
		t.Error("the conversation is not marked cacheable, so only the static prefix is cached")
	}

	// And nowhere else: breakpoints are a limited resource (four per request),
	// so spending them on earlier messages would be wasteful as well as wrong.
	if n := bytes.Count([]byte(mustJSON(t, body["messages"])), []byte("cache_control")); n != 1 {
		t.Errorf("expected exactly one breakpoint in the messages, got %d", n)
	}
}

func TestNoPromptCacheSendsNoCacheControl(t *testing.T) {
	req := cacheRequest()
	req.NoPromptCache = true

	body := anthropicRequestBody(t, req)
	if raw := mustJSON(t, body); strings.Contains(raw, "cache_control") {
		t.Fatalf("cache_control was sent despite NoPromptCache: %s", raw)
	}
	// The system prompt still has to arrive. The block form is used either
	// way, so that there is only one request shape to reason about.
	if _, ok := body["system"].([]any); !ok {
		t.Fatalf("system should still be a list of blocks, got %T", body["system"])
	}
}

// TestEmptySystemPromptIsOmitted: an empty block would be rejected, and there
// is nothing to cache.
func TestEmptySystemPromptIsOmitted(t *testing.T) {
	req := cacheRequest()
	req.System = ""

	body := anthropicRequestBody(t, req)
	if _, present := body["system"]; present {
		t.Fatalf("an empty system prompt should be omitted entirely, got %v", body["system"])
	}
}

func TestAnthropicMaxTokens(t *testing.T) {
	body := anthropicRequestBody(t, cacheRequest())
	if got := body["max_tokens"]; got != float64(anthropicDefaultMaxTokens) {
		t.Errorf("default max_tokens = %v, want %d", got, anthropicDefaultMaxTokens)
	}

	req := cacheRequest()
	req.MaxTokens = 16000
	body = anthropicRequestBody(t, req)
	if got := body["max_tokens"]; got != float64(16000) {
		t.Errorf("max_tokens = %v, want 16000 — Request.MaxTokens was ignored", got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
