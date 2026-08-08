package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decodo/tyci/stream"
)

// Test chatUsage.UnmarshalJSON with various inputs
func TestChatUsage_UnmarshalJSON_StandardFields(t *testing.T) {
	data := []byte(`{
		"prompt_tokens": 100,
		"completion_tokens": 50,
		"reasoning_tokens": 10,
		"cache_read_input_tokens": 20,
		"cache_creation_tokens": 5
	}`)

	var u chatUsage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if u.InputTokens != 100 {
		t.Errorf("InputTokens: got %d, want 100", u.InputTokens)
	}
	if u.OutputTokens != 50 {
		t.Errorf("OutputTokens: got %d, want 50", u.OutputTokens)
	}
	if u.ReasoningTokens != 10 {
		t.Errorf("ReasoningTokens: got %d, want 10", u.ReasoningTokens)
	}
	if u.CacheReadInputTokens != 20 {
		t.Errorf("CacheReadInputTokens: got %d, want 20", u.CacheReadInputTokens)
	}
	if u.CacheCreateInputTokens != 5 {
		t.Errorf("CacheCreateInputTokens: got %d, want 5", u.CacheCreateInputTokens)
	}
}

func TestChatUsage_UnmarshalJSON_AltFields(t *testing.T) {
	data := []byte(`{
		"input_tokens": 100,
		"output_tokens": 50
	}`)

	var u chatUsage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if u.InputTokensAlt != 100 {
		t.Errorf("InputTokensAlt: got %d, want 100", u.InputTokensAlt)
	}
	if u.OutputTokensAlt != 50 {
		t.Errorf("OutputTokensAlt: got %d, want 50", u.OutputTokensAlt)
	}
}

func TestChatUsage_UnmarshalJSON_PromptCacheHitTokens(t *testing.T) {
	data := []byte(`{
		"prompt_tokens": 100,
		"completion_tokens": 50,
		"prompt_cache_hit_tokens": 30
	}`)

	var u chatUsage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if u.CacheReadInputTokens != 30 {
		t.Errorf("CacheReadInputTokens: got %d, want 30", u.CacheReadInputTokens)
	}
}

func TestChatUsage_UnmarshalJSON_PromptCacheMissTokens(t *testing.T) {
	data := []byte(`{
		"prompt_tokens": 100,
		"completion_tokens": 50,
		"prompt_cache_miss_tokens": 15
	}`)

	var u chatUsage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if u.CacheCreateInputTokens != 15 {
		t.Errorf("CacheCreateInputTokens: got %d, want 15", u.CacheCreateInputTokens)
	}
}

func TestChatUsage_UnmarshalJSON_CachedTokens(t *testing.T) {
	data := []byte(`{
		"prompt_tokens": 100,
		"completion_tokens": 50,
		"cached_tokens": 25
	}`)

	var u chatUsage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if u.CacheReadInputTokens != 25 {
		t.Errorf("CacheReadInputTokens: got %d, want 25", u.CacheReadInputTokens)
	}
}

func TestChatUsage_UnmarshalJSON_PromptTokensDetails(t *testing.T) {
	data := []byte(`{
		"prompt_tokens": 100,
		"completion_tokens": 50,
		"prompt_tokens_details": {"cached_tokens": 40}
	}`)

	var u chatUsage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if u.CacheReadInputTokens != 40 {
		t.Errorf("CacheReadInputTokens: got %d, want 40", u.CacheReadInputTokens)
	}
}

func TestChatUsage_UnmarshalJSON_CompletionTokensDetails(t *testing.T) {
	data := []byte(`{
		"prompt_tokens": 100,
		"completion_tokens": 50,
		"completion_tokens_details": {"reasoning_tokens": 15}
	}`)

	var u chatUsage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if u.ReasoningTokens != 15 {
		t.Errorf("ReasoningTokens: got %d, want 15", u.ReasoningTokens)
	}
}

func TestChatUsage_UnmarshalJSON_ExistingValuesNotOverwritten(t *testing.T) {
	data := []byte(`{
		"prompt_tokens": 100,
		"completion_tokens": 50,
		"reasoning_tokens": 10,
		"cache_read_input_tokens": 20,
		"cache_creation_tokens": 5,
		"prompt_cache_hit_tokens": 99,
		"prompt_cache_miss_tokens": 99,
		"completion_tokens_details": {"reasoning_tokens": 99}
	}`)

	var u chatUsage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	// Existing values should NOT be overwritten by extra fields
	if u.ReasoningTokens != 10 {
		t.Errorf("ReasoningTokens should remain 10, got %d", u.ReasoningTokens)
	}
	if u.CacheReadInputTokens != 20 {
		t.Errorf("CacheReadInputTokens should remain 20, got %d", u.CacheReadInputTokens)
	}
	if u.CacheCreateInputTokens != 5 {
		t.Errorf("CacheCreateInputTokens should remain 5, got %d", u.CacheCreateInputTokens)
	}
}

func TestChatUsage_UnmarshalJSON_EmptyObject(t *testing.T) {
	data := []byte(`{}`)

	var u chatUsage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	// All fields should be zero
	if u.InputTokens != 0 || u.OutputTokens != 0 || u.ReasoningTokens != 0 {
		t.Errorf("expected zero values, got %+v", u)
	}
}

func TestChatUsage_UnmarshalJSON_InvalidJSON(t *testing.T) {
	data := []byte(`{invalid}`)

	var u chatUsage
	if err := json.Unmarshal(data, &u); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// Test doer — the client-selection rule that replaced the api-package context
// key. The three cases below are the same three guarantees the deleted
// context-lookup tests asserted, restated against the injection path (a field
// on the streamer, filled from connector.Endpoint.HTTP).

// Same guarantee as the deleted default-client case: nothing injected ->
// shared default client.
func TestDoer_NoInjectionUsesDefaultClient(t *testing.T) {
	client := doer(nil)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client != HTTPDoer(defaultClient) {
		t.Error("expected default client")
	}
}

// Same guarantee as the deleted override case: an explicitly supplied client
// is the one used.
func TestDoer_InjectedClientWins(t *testing.T) {
	customClient := &http.Client{}
	if client := doer(customClient); client != HTTPDoer(customClient) {
		t.Error("expected the injected client")
	}
	// Any HTTPDoer, not just *http.Client.
	stub := &stubDoer{}
	if client := doer(stub); client != HTTPDoer(stub) {
		t.Error("expected the injected non-*http.Client doer")
	}
}

// Same guarantee as the deleted nil-in-context case: an explicit nil client
// must be ignored in favor of the default, not used. The old context lookup
// guarded this with `cl != nil`; doer keeps the guard so a typed-nil
// *http.Client (easy to produce via providers.Deps{HTTP: someNilClientVar})
// degrades instead of panicking inside net/http.
func TestDoer_TypedNilClientUsesDefaultClient(t *testing.T) {
	var nilClient *http.Client
	if client := doer(nilClient); client != HTTPDoer(defaultClient) {
		t.Error("expected default client when a typed-nil *http.Client is injected")
	}
}

// Test StreamChat
func TestStreamChat_TextResponse(t *testing.T) {
	sseEvents := `data: {"choices":[{"delta":{"content":"Hello"}}]}
data: {"choices":[{"delta":{"content":" world"}}]}
data: {"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}
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

	body := ChatRequest{
		Model:    "gpt-4",
		Stream:   true,
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}

	err := ChatStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
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

	if len(texts) != 2 || texts[0] != "Hello" || texts[1] != " world" {
		t.Errorf("expected text deltas ['Hello', ' world'], got %v", texts)
	}
	if finish.Reason != "stop" {
		t.Errorf("expected reason 'stop', got %q", finish.Reason)
	}
	if finish.Usage.Input != 10 {
		t.Errorf("expected input tokens 10, got %d", finish.Usage.Input)
	}
	if finish.Usage.Output != 5 {
		t.Errorf("expected output tokens 5, got %d", finish.Usage.Output)
	}
}

func TestStreamChat_ToolCalls(t *testing.T) {
	sseEvents := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"loc"}}]}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ation\":\"NY\"}"}}]}}]}
data: {"choices":[{"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}
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

	body := ChatRequest{
		Model:    "gpt-4",
		Stream:   true,
		Messages: []ChatMessage{{Role: "user", Content: "weather?"}},
	}

	err := ChatStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
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
	if toolCalls[0].ID != "call_1" {
		t.Errorf("expected ID 'call_1', got %q", toolCalls[0].ID)
	}
	if toolCalls[0].Name != "get_weather" {
		t.Errorf("expected Name 'get_weather', got %q", toolCalls[0].Name)
	}
}

// TestStreamChat_ToolCalls_Malformed verifies the parser drops tool calls
// without a function name (which would otherwise trigger
// "tool_calls[0] is missing a function name" 400s on strict providers) and
// back-fills a stable ID when the provider returns none (so the matching
// tool-result message can carry the required tool_call_id).
func TestStreamChat_ToolCalls_Malformed(t *testing.T) {
	sseEvents := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"","arguments":"{}"}}]}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":1,"type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}}]}}]}
data: {"choices":[{"finish_reason":"tool_calls"}]}
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

	body := ChatRequest{
		Model:    "gpt-4",
		Stream:   true,
		Messages: []ChatMessage{{Role: "user", Content: "go"}},
	}

	if err := (ChatStreamer{}).Stream(testCtx(), "test-key", server.URL, body, emit); err != nil {
		t.Fatalf("ChatStreamer.Stream: %v", err)
	}

	var toolCalls []stream.ToolCall
	for _, e := range events {
		if tc, ok := e.(stream.ToolCall); ok {
			toolCalls = append(toolCalls, tc)
		}
	}

	// Only the named tool call survives; the nameless one is dropped.
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call (nameless dropped), got %d", len(toolCalls))
	}
	if toolCalls[0].Name != "bash" {
		t.Errorf("expected Name 'bash', got %q", toolCalls[0].Name)
	}
	// ID was missing from the stream — parser must back-fill a stable one.
	if toolCalls[0].ID == "" {
		t.Errorf("expected non-empty back-filled ID, got empty")
	}
}

func TestStreamChat_Reasoning(t *testing.T) {
	sseEvents := `data: {"choices":[{"delta":{"reasoning":"thinking..."}}]}
data: {"choices":[{"delta":{"content":"Hello"}}]}
data: {"choices":[{"finish_reason":"stop"}]}
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

	body := ChatRequest{
		Model:    "mimo-v2.5-pro",
		Stream:   true,
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}

	err := ChatStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var thinking []string
	var texts []string
	for _, e := range events {
		switch v := e.(type) {
		case stream.ThinkingDelta:
			thinking = append(thinking, v.Text)
		case stream.TextDelta:
			texts = append(texts, v.Text)
		}
	}

	if len(thinking) != 1 || thinking[0] != "thinking..." {
		t.Errorf("expected thinking 'thinking...', got %v", thinking)
	}
	if len(texts) != 1 || texts[0] != "Hello" {
		t.Errorf("expected text 'Hello', got %v", texts)
	}
}

func TestStreamChat_ReasoningContent(t *testing.T) {
	sseEvents := `data: {"choices":[{"delta":{"reasoning_content":"thinking..."}}]}
data: {"choices":[{"delta":{"content":"Hello"}}]}
data: {"choices":[{"finish_reason":"stop"}]}
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

	body := ChatRequest{
		Model:    "gpt-4",
		Stream:   true,
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}

	err := ChatStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var thinking []string
	var texts []string
	for _, e := range events {
		switch v := e.(type) {
		case stream.ThinkingDelta:
			thinking = append(thinking, v.Text)
		case stream.TextDelta:
			texts = append(texts, v.Text)
		}
	}

	if len(thinking) != 1 || thinking[0] != "thinking..." {
		t.Errorf("expected thinking 'thinking...', got %v", thinking)
	}
	if len(texts) != 1 || texts[0] != "Hello" {
		t.Errorf("expected text 'Hello', got %v", texts)
	}
}

func TestStreamChat_EmptyStream(t *testing.T) {
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

	body := ChatRequest{
		Model:    "gpt-4",
		Stream:   true,
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}

	err := ChatStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
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
}

func TestStreamChat_Error429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	emit := func(e stream.Event) error { return nil }
	body := ChatRequest{Model: "gpt-4", Stream: true, Messages: []ChatMessage{{Role: "user", Content: "hi"}}}

	err := ChatStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
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

func TestStreamChat_Error500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	emit := func(e stream.Event) error { return nil }
	body := ChatRequest{Model: "gpt-4", Stream: true, Messages: []ChatMessage{{Role: "user", Content: "hi"}}}

	err := ChatStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
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

func TestStreamChat_Error400(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer server.Close()

	emit := func(e stream.Event) error { return nil }
	body := ChatRequest{Model: "gpt-4", Stream: true, Messages: []ChatMessage{{Role: "user", Content: "hi"}}}

	err := ChatStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	// 400 is not retryable
	var re *RetryableError
	if as(err, &re) {
		t.Error("400 should not be RetryableError")
	}
}

func TestStreamChat_UsageAltFields(t *testing.T) {
	sseEvents := `data: {"choices":[{"delta":{"content":"Hi"}}],"usage":{"input_tokens":10,"output_tokens":5}}
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

	body := ChatRequest{
		Model:    "gpt-4",
		Stream:   true,
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}

	err := ChatStreamer{}.Stream(testCtx(), "test-key", server.URL, body, emit)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
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
	// input_tokens maps to Input, output_tokens maps to Output
	if finish.Usage.Input != 10 {
		t.Errorf("expected input 10, got %d", finish.Usage.Input)
	}
	if finish.Usage.Output != 5 {
		t.Errorf("expected output 5, got %d", finish.Usage.Output)
	}
}

// testCtx returns a background context for tests. It lives in this untagged
// file (it used to sit in anthropic_test.go, behind //go:build !noanthropic)
// so that `go test -tags "noanthropic nogemini" ./api/` still compiles.
func testCtx() context.Context {
	return context.Background()
}

// Helper to wrap errors.As for testing
func as(err error, target any) bool {
	switch t := target.(type) {
	case **RetryableError:
		if re, ok := err.(*RetryableError); ok {
			*t = re
			return true
		}
	}
	return false
}

// =============================================================================
// HTTPDoer injection
// =============================================================================

// stubDoer is an HTTPDoer that answers from memory and remembers what it got.
type stubDoer struct {
	got   *http.Request
	body  string
	calls int
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	d.got = req
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Request:    req,
	}, nil
}

const stubSSE = `data: {"choices":[{"delta":{"content":"from-field"}}]}
data: {"choices":[{"finish_reason":"stop"}]}
data: [DONE]
`

// The HTTP field is a real injection point AND it outranks the shared default
// client. Both halves matter: the field is what connector.Endpoint carries
// (populated by providers.Deps.HTTP), and nothing else may quietly override it.
func TestChatStreamer_HTTPFieldWinsOverDefaultClient(t *testing.T) {
	// A perfectly usable server the default client could reach — the injected
	// doer must mean it is never contacted.
	var defaultClientHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultClientHits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	ctx := context.Background()

	doer := &stubDoer{body: stubSSE}
	var texts []string
	emit := func(e stream.Event) error {
		if td, ok := e.(stream.TextDelta); ok {
			texts = append(texts, td.Text)
		}
		return nil
	}

	s := ChatStreamer{HTTP: doer, Headers: map[string]string{"X-Tyci-Test": "1"}}
	if err := s.Stream(ctx, "test-key", server.URL, ChatRequest{Model: "gpt-4"}, emit); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if doer.calls != 1 {
		t.Fatalf("injected doer got %d requests, want 1", doer.calls)
	}
	if defaultClientHits != 0 {
		t.Errorf("the default client was used %d times despite the HTTP field", defaultClientHits)
	}
	if len(texts) != 1 || texts[0] != "from-field" {
		t.Errorf("text deltas = %v, want [from-field] (response came from the wrong client)", texts)
	}
	// Endpoint.Headers land on the request, after the protocol defaults.
	if got := doer.got.Header.Get("X-Tyci-Test"); got != "1" {
		t.Errorf("X-Tyci-Test = %q, want %q", got, "1")
	}
	if got := doer.got.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer test-key")
	}
}

// With HTTP left nil the shared default client takes the request. This is the
// production path for every provider built without its own Deps.HTTP, and it
// is what api.defaultClient exists for now that the context lookup is gone.
func TestChatStreamer_NilHTTPFallsBackToDefaultClient(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	// No hook to point anywhere: httptest speaks plain HTTP on 127.0.0.1, so
	// the real shared client reaches the server unaided. That the streamer
	// picks that client rather than some other one is asserted directly, and
	// without a global, by TestDoer_NoInjectionUsesDefaultClient.
	emit := func(stream.Event) error { return nil }
	if err := (ChatStreamer{}).Stream(context.Background(), "k", server.URL, ChatRequest{Model: "gpt-4"}, emit); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if hits != 1 {
		t.Fatalf("default client got %d requests, want 1", hits)
	}
}
