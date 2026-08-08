package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
)

// silentDisplay is a minimal Display implementation for tests.
type silentDisplay struct{}

func (s *silentDisplay) Request(string)                     {}
func (s *silentDisplay) Thinking(string)                    {}
func (s *silentDisplay) Text(string)                        {}
func (s *silentDisplay) ToolCallStart(string)               {}
func (s *silentDisplay) ToolCallDelta(string)               {}
func (s *silentDisplay) ToolCallEnd(string, string)         {}
func (s *silentDisplay) ToolFinish()                        {}
func (s *silentDisplay) ToolBlock(string)                   {}
func (s *silentDisplay) Summary(stream.Usage, stream.Stats) {}
func (s *silentDisplay) Total(stream.Usage)                 {}
func (s *silentDisplay) Error(error)                        {}
func (s *silentDisplay) End()                               {}

type mockProvider struct {
	chunks []string
}

func (m *mockProvider) Name() string         { return "mock" }
func (m *mockProvider) IsConfigured() bool   { return true }
func (m *mockProvider) Models() []string     { return []string{"mock-1"} }
func (m *mockProvider) FreeModels() []string { return nil }

func (m *mockProvider) Stream(ctx context.Context, req providers.Request) (<-chan stream.Event, error) {
	ch := make(chan stream.Event, 4)
	go func() {
		defer close(ch)
		for _, c := range m.chunks {
			select {
			case ch <- stream.TextDelta{Text: c}:
			case <-ctx.Done():
				return
			}
		}
		ch <- stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}}
	}()
	return ch, nil
}

// mockToolProvider emits a predefined sequence of events once, then returns empty finish.
type mockToolProvider struct {
	mu     sync.Mutex
	events []stream.Event
	called bool
}

func (m *mockToolProvider) Name() string         { return "mock-tool" }
func (m *mockToolProvider) IsConfigured() bool   { return true }
func (m *mockToolProvider) Models() []string     { return []string{"mock-tool-1"} }
func (m *mockToolProvider) FreeModels() []string { return nil }

func (m *mockToolProvider) Stream(ctx context.Context, req providers.Request) (<-chan stream.Event, error) {
	m.mu.Lock()
	if m.called {
		m.mu.Unlock()
		// Subsequent calls: no tools, just finish
		ch := make(chan stream.Event, 1)
		ch <- stream.Finish{Usage: stream.Usage{Input: 0, Output: 0}}
		close(ch)
		return ch, nil
	}
	m.called = true
	events := m.events
	m.mu.Unlock()

	ch := make(chan stream.Event, 4)
	go func() {
		defer close(ch)
		for _, e := range events {
			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// mockToolRunner records tool calls and returns canned results.
type mockToolRunner struct {
	mu      sync.Mutex
	calls   []toolCallRecord
	results map[string]string
}

type toolCallRecord struct {
	Name string
	Args map[string]any
}

func newMockToolRunner() *mockToolRunner {
	return &mockToolRunner{
		results: make(map[string]string),
	}
}

func (r *mockToolRunner) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, toolCallRecord{Name: name, Args: args})
	result := r.results[name]
	r.mu.Unlock()
	return result, nil
}

func (r *mockToolRunner) SetResult(name, result string) {
	r.mu.Lock()
	r.results[name] = result
	r.mu.Unlock()
}

// captureDisplay implements agent.Sink and records all calls for assertion.
type captureDisplay struct {
	mu             sync.Mutex
	thinking       []string
	text           []string
	toolCallStarts []string
	toolCallDeltas []string
	toolCallEnds   []struct{ Name, Result string }
	toolBlocks     []string
	summaries      []stream.Usage
	totals         []stream.Usage
	errors         []error
}

func newCaptureDisplay() *captureDisplay {
	return &captureDisplay{}
}

func (c *captureDisplay) Thinking(text string) {
	c.mu.Lock()
	c.thinking = append(c.thinking, text)
	c.mu.Unlock()
}

func (c *captureDisplay) Text(text string) {
	c.mu.Lock()
	c.text = append(c.text, text)
	c.mu.Unlock()
}

func (c *captureDisplay) ToolCallStart(name string) {
	c.mu.Lock()
	c.toolCallStarts = append(c.toolCallStarts, name)
	c.mu.Unlock()
}

func (c *captureDisplay) ToolCallDelta(delta string) {
	c.mu.Lock()
	c.toolCallDeltas = append(c.toolCallDeltas, delta)
	c.mu.Unlock()
}

func (c *captureDisplay) ToolCallEnd(name, result string) {
	c.mu.Lock()
	c.toolCallEnds = append(c.toolCallEnds, struct{ Name, Result string }{name, result})
	c.mu.Unlock()
}

func (c *captureDisplay) ToolBlock(msg string) {
	c.mu.Lock()
	c.toolBlocks = append(c.toolBlocks, msg)
	c.mu.Unlock()
}

func (c *captureDisplay) Summary(usage stream.Usage, stats stream.Stats) {
	c.mu.Lock()
	c.summaries = append(c.summaries, usage)
	c.mu.Unlock()
}

func (c *captureDisplay) Total(usage stream.Usage) {
	c.mu.Lock()
	c.totals = append(c.totals, usage)
	c.mu.Unlock()
}

func (c *captureDisplay) Error(err error) {
	c.mu.Lock()
	c.errors = append(c.errors, err)
	c.mu.Unlock()
}
func (c *captureDisplay) End() {}

func (c *captureDisplay) Request(content string) {}
func (c *captureDisplay) ToolFinish()            {}

func TestRunAppendsAssistantMessage(t *testing.T) {
	p := &mockProvider{chunks: []string{"Hello", " world"}}
	d := &silentDisplay{}
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "Hi"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{Model: "mock-1", MaxRetries: 1}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("expected assistant role, got %q", msgs[1].Role)
	}
	if len(msgs[1].Content) != 1 || msgs[1].Content[0].Type != "text" {
		t.Fatalf("expected one text block, got %#v", msgs[1].Content)
	}
	if msgs[1].Content[0].Text != "Hello world" {
		t.Errorf("expected %q, got %q", "Hello world", msgs[1].Content[0].Text)
	}
}

func TestRunSkipsEmptyAssistantMessage(t *testing.T) {
	p := &mockProvider{chunks: nil}
	d := &silentDisplay{}
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "Hi"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{Model: "mock-1", MaxRetries: 1}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %#v", len(msgs), msgs)
	}
}

func TestRun_ToolCall_ShowsToolBlockDuringStream(t *testing.T) {
	p := &mockToolProvider{
		events: []stream.Event{
			stream.TextDelta{Text: "I'll look that up."},
			stream.ToolCallStart{ID: "tc1", Name: "read"},
			stream.ToolCallDelta{ID: "tc1", Delta: `{"path": "file.go"}`},
			stream.ToolCall{ID: "tc1", Name: "read", Arguments: `{"path": "file.go"}`},
			stream.Finish{Usage: stream.Usage{Input: 10, Output: 5}},
		},
	}
	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("read", "file content")
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "read file.go"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "mock-tool-1",
		MaxRetries: 1,
		Tools:      runner,
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Should have shown ToolBlock with waiting message
	if len(d.toolBlocks) == 0 {
		t.Fatal("expected at least one ToolBlock call")
	}
	if d.toolBlocks[0] != "⏳ waiting for tools..." {
		t.Errorf("expected ToolBlock message %q, got %q", "⏳ waiting for tools...", d.toolBlocks[0])
	}

	// Should have shown ToolCallStart
	if len(d.toolCallStarts) == 0 {
		t.Fatal("expected ToolCallStart")
	}
	if d.toolCallStarts[0] != "read" {
		t.Errorf("expected ToolCallStart with 'read', got %q", d.toolCallStarts[0])
	}

	// Should have shown tool result
	if len(d.toolCallEnds) == 0 {
		t.Fatal("expected ToolCallEnd")
	}
	if d.toolCallEnds[0].Name != "read" {
		t.Errorf("expected ToolCallEnd name 'read', got %q", d.toolCallEnds[0].Name)
	}
	if d.toolCallEnds[0].Result != "file content" {
		t.Errorf("expected ToolCallEnd result 'file content', got %q", d.toolCallEnds[0].Result)
	}

	// Should have shown summary (usage)
	if len(d.summaries) == 0 {
		t.Fatal("expected Summary call")
	}
	if d.summaries[0].Input != 10 {
		t.Errorf("expected Input 10, got %d", d.summaries[0].Input)
	}

	// Messages should include assistant + tool result
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (user, assistant, tool), got %d", len(msgs))
	}
	if msgs[2].Role != "toolResult" {
		t.Errorf("expected toolResult role, got %q", msgs[2].Role)
	}
	// Check tool result content
	if len(msgs[2].Content) != 1 || msgs[2].Content[0].Type != "text" {
		t.Fatalf("expected one text block in tool result, got %#v", msgs[2].Content)
	}
	if msgs[2].Content[0].Text != "file content" {
		t.Errorf("expected tool result text %q, got %q", "file content", msgs[2].Content[0].Text)
	}
}

func TestRun_ToolCall_NoToolBlockWithoutTools(t *testing.T) {
	p := &mockToolProvider{
		events: []stream.Event{
			stream.TextDelta{Text: "No tools needed."},
			stream.Finish{Usage: stream.Usage{Input: 5, Output: 3}},
		},
	}
	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "Hello"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "mock-tool-1",
		MaxRetries: 1,
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(d.toolBlocks) > 0 {
		t.Errorf("expected no ToolBlock when no tools, got %v", d.toolBlocks)
	}
	if len(d.toolCallStarts) > 0 {
		t.Errorf("expected no ToolCallStart when no tools, got %v", d.toolCallStarts)
	}
}

func TestRun_ToolCall_MultipleTools(t *testing.T) {
	p := &mockToolProvider{
		events: []stream.Event{
			stream.ToolCallStart{ID: "tc1", Name: "read"},
			stream.ToolCallDelta{ID: "tc1", Delta: `{"path": "a.go"}`},
			stream.ToolCall{ID: "tc1", Name: "read", Arguments: `{"path": "a.go"}`},
			stream.ToolCallStart{ID: "tc2", Name: "bash"},
			stream.ToolCallDelta{ID: "tc2", Delta: `{"command": "ls"}`},
			stream.ToolCall{ID: "tc2", Name: "bash", Arguments: `{"command": "ls"}`},
			stream.Finish{Usage: stream.Usage{Input: 20, Output: 10}},
		},
	}
	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("read", "content of a.go")
	runner.SetResult("bash", "file1\nfile2")
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "list and read"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "mock-tool-1",
		MaxRetries: 1,
		Tools:      runner,
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// ToolBlock shown exactly once (on first tool)
	if len(d.toolBlocks) != 1 {
		t.Fatalf("expected exactly 1 ToolBlock, got %d: %v", len(d.toolBlocks), d.toolBlocks)
	}
	if d.toolBlocks[0] != "⏳ waiting for tools..." {
		t.Errorf("expected waiting message, got %q", d.toolBlocks[0])
	}

	// Both tools should have starts
	if len(d.toolCallStarts) != 2 {
		t.Fatalf("expected 2 ToolCallStarts, got %d: %v", len(d.toolCallStarts), d.toolCallStarts)
	}
	if d.toolCallStarts[0] != "read" {
		t.Errorf("expected first tool 'read', got %q", d.toolCallStarts[0])
	}
	if d.toolCallStarts[1] != "bash" {
		t.Errorf("expected second tool 'bash', got %q", d.toolCallStarts[1])
	}

	// Both tool results
	if len(d.toolCallEnds) != 2 {
		t.Fatalf("expected 2 ToolCallEnds, got %d", len(d.toolCallEnds))
	}

	// Messages: user + assistant + tool1 + tool2
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
}

func TestRun_ToolCall_TextAndTools(t *testing.T) {
	p := &mockToolProvider{
		events: []stream.Event{
			stream.TextDelta{Text: "I'll check that file. "},
			stream.ToolCallStart{ID: "tc1", Name: "read"},
			stream.ToolCallDelta{ID: "tc1", Delta: `{"path": "x.go"}`},
			stream.ToolCall{ID: "tc1", Name: "read", Arguments: `{"path": "x.go"}`},
			stream.Finish{Usage: stream.Usage{Input: 15, Output: 7}},
		},
	}
	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("read", "package main")
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "read x.go"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "mock-tool-1",
		MaxRetries: 1,
		Tools:      runner,
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Text should be captured
	if len(d.text) == 0 {
		t.Fatal("expected text output")
	}
	fullText := strings.Join(d.text, "")
	if !strings.Contains(fullText, "I'll check that file.") {
		t.Errorf("expected text, got %q", fullText)
	}

	// ToolBlock should be shown
	if len(d.toolBlocks) != 1 {
		t.Fatalf("expected 1 ToolBlock, got %d", len(d.toolBlocks))
	}
}

func TestRun_ToolCall_ToolCallWithoutDelta(t *testing.T) {
	// Some providers send ToolCall directly without ToolCallStart/Delta
	p := &mockToolProvider{
		events: []stream.Event{
			stream.ToolCall{ID: "tc1", Name: "bash", Arguments: `{"command": "echo hi"}`},
			stream.Finish{Usage: stream.Usage{Input: 5, Output: 2}},
		},
	}
	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("bash", "hi")
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "echo"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "mock-tool-1",
		MaxRetries: 1,
		Tools:      runner,
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// ToolBlock should NOT be shown because there was no ToolCallStart
	// (ToolBlock only triggers on ToolCallStart)
	if len(d.toolBlocks) > 0 {
		t.Errorf("expected no ToolBlock when no ToolCallStart, got %v", d.toolBlocks)
	}

	// But ToolCallStart should still be shown (from the loop after stream)
	if len(d.toolCallStarts) == 0 {
		t.Fatal("expected ToolCallStart even without start event")
	}
	if d.toolCallStarts[0] != "bash" {
		t.Errorf("expected 'bash', got %q", d.toolCallStarts[0])
	}
}

func TestRun_ToolCall_EmptyResult(t *testing.T) {
	p := &mockToolProvider{
		events: []stream.Event{
			stream.ToolCallStart{ID: "tc1", Name: "bash"},
			stream.ToolCall{ID: "tc1", Name: "bash", Arguments: `{"command": "false"}`},
			stream.Finish{Usage: stream.Usage{Input: 5, Output: 2}},
		},
	}
	d := newCaptureDisplay()
	runner := newMockToolRunner()
	// No result set → Run returns empty string, no error
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "run"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "mock-tool-1",
		MaxRetries: 1,
		Tools:      runner,
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Tool result should be empty string
	if len(d.toolCallEnds) != 1 {
		t.Fatalf("expected 1 ToolCallEnd, got %d", len(d.toolCallEnds))
	}
	if d.toolCallEnds[0].Result != "" {
		t.Errorf("expected empty result, got %q", d.toolCallEnds[0].Result)
	}
}

func TestRun_ToolCall_StreamError(t *testing.T) {
	p := &mockToolProvider{
		events: []stream.Event{
			stream.StreamError{Err: errors.New("connection lost")},
		},
	}
	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	_, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "mock-tool-1",
		MaxRetries: 1,
	})
	if err == nil {
		t.Fatal("expected error from stream")
	}
	if !strings.Contains(err.Error(), "connection lost") {
		t.Errorf("expected 'connection lost', got %v", err)
	}
}

// TestWriteSessionEvents verifies that session events are written correctly.
func TestWriteSessionEvents(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.jsonl"

	s, err := session.Open(path, "/test", "mock-1", "mock")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}

	err = s.WriteMessage("assistant", []session.ContentBlock{
		{Type: "text", Text: "Hello"},
		{Type: "toolCall", ID: "tc1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
	}, &session.MessageOptions{
		Provider: "mock",
		Model:    "mock-1",
		Usage:    &session.Usage{Input: 10, Output: 5, Reasoning: 2, TotalTokens: 17},
	})
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	err = s.WriteMessage("toolResult", []session.ContentBlock{
		{Type: "text", Text: "file1\nfile2", ToolCallID: "tc1", ToolName: "bash"},
	}, nil)
	if err != nil {
		t.Fatalf("WriteMessage toolResult: %v", err)
	}

	err = s.WriteSessionEnd("ok", 0, &session.Usage{Input: 10, Output: 5, TotalTokens: 15})
	if err != nil {
		t.Fatalf("WriteSessionEnd: %v", err)
	}
	s.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (header, assistant, toolResult, session_end), got %d", len(lines))
	}

	if !strings.Contains(lines[0], `"type":"session"`) {
		t.Errorf("expected session header, got: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"type":"message"`) {
		t.Errorf("expected message event, got: %s", lines[1])
	}
	if !strings.Contains(lines[1], `"role":"assistant"`) {
		t.Errorf("expected assistant role, got: %s", lines[1])
	}
	if !strings.Contains(lines[2], `"role":"toolResult"`) {
		t.Errorf("expected toolResult role, got: %s", lines[2])
	}
	if !strings.Contains(lines[3], `"type":"session_end"`) {
		t.Errorf("expected session_end, got: %s", lines[3])
	}
}

// ─── Fallback tests ──────────────────────────────────────────────────────

// mockFailingProvider always returns an error from Stream().
type mockFailingProvider struct {
	name  string
	model string
	err   error
}

func (m *mockFailingProvider) Name() string         { return m.name }
func (m *mockFailingProvider) IsConfigured() bool   { return true }
func (m *mockFailingProvider) Models() []string     { return []string{m.model} }
func (m *mockFailingProvider) FreeModels() []string { return nil }

func (m *mockFailingProvider) Stream(ctx context.Context, req providers.Request) (<-chan stream.Event, error) {
	if m.err == nil {
		return nil, errors.New("mockFailingProvider: no error configured")
	}
	return nil, m.err
}

// newFailingProvider is a small helper for tests that only need an
// always-failing provider with a plain (non-retryable) error message.
func newFailingProvider(name, model, msg string) *mockFailingProvider {
	return &mockFailingProvider{name: name, model: model, err: errors.New(msg)}
}

// mockTextProvider returns fixed text chunks then finishes (no tools).
type mockTextProvider struct {
	name   string
	model  string
	chunks []string
}

func (m *mockTextProvider) Name() string         { return m.name }
func (m *mockTextProvider) IsConfigured() bool   { return true }
func (m *mockTextProvider) Models() []string     { return []string{m.model} }
func (m *mockTextProvider) FreeModels() []string { return nil }

func (m *mockTextProvider) Stream(ctx context.Context, req providers.Request) (<-chan stream.Event, error) {
	ch := make(chan stream.Event, 4)
	go func() {
		defer close(ch)
		for _, c := range m.chunks {
			select {
			case ch <- stream.TextDelta{Text: c}:
			case <-ctx.Done():
				return
			}
		}
		ch <- stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}}
	}()
	return ch, nil
}

// mockToolProvider returns events once, then returns empty finish on subsequent calls.
// This is already defined above but we extend it here for the tests.

func TestRunFallbackPrimaryFailsFallbackSucceeds(t *testing.T) {
	// Primary provider returns error, fallback succeeds with text
	primary := &mockFailingProvider{name: "fb-primary", model: "primary-1", err: errors.New("server error 500")}

	fallback := &mockTextProvider{name: "fb-fallback", model: "fb-1", chunks: []string{"fallback response"}}

	// Register the fallback so FindModel can resolve it
	providers.Register(fallback)

	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "Hello"}},
	}}

	cfg := Config{
		Model:          "primary-1",
		MaxRetries:     1,
		FallbackModels: []string{"fb-fallback/fb-1"},
	}

	_, err := Run(context.Background(), primary, d, &msgs, cfg)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Should have produced text from the fallback
	fullText := strings.Join(d.text, "")
	if !strings.Contains(fullText, "fallback response") {
		t.Errorf("expected 'fallback response', got %q", fullText)
	}

	// Messages should include fallback assistant response
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (user, assistant), got %d", len(msgs))
	}
	if len(msgs[1].Content) == 0 || msgs[1].Content[0].Text != "fallback response" {
		t.Errorf("expected fallback response text, got %+v", msgs[1].Content)
	}
}

func TestRunFallbackAllFallbacksFail(t *testing.T) {
	// Primary fails, all fallbacks also fail
	primary := &mockFailingProvider{name: "fb-p1", model: "p1", err: errors.New("primary dead")}

	fallback1 := &mockFailingProvider{name: "fb-f1", model: "f1", err: errors.New("fb1 dead")}
	fallback2 := &mockFailingProvider{name: "fb-f2", model: "f2", err: errors.New("fb2 dead")}

	providers.Register(fallback1)
	providers.Register(fallback2)

	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "Hello"}},
	}}

	cfg := Config{
		Model:          "p1",
		MaxRetries:     1,
		FallbackModels: []string{"fb-f1/f1", "fb-f2/f2"},
	}

	_, err := Run(context.Background(), primary, d, &msgs, cfg)
	if err == nil {
		t.Fatal("expected error when all fallbacks fail")
	}
	if !strings.Contains(err.Error(), "fb2 dead") {
		t.Errorf("expected last fallback error, got %v", err)
	}
}

func TestRunFallbackPrimaryFailsWithNoFallbacks(t *testing.T) {
	// Primary fails with non-retryable error, no fallbacks configured
	primary := &mockFailingProvider{name: "fb-nofb", model: "nofb-1", err: errors.New("non-retryable")}

	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "Hello"}},
	}}

	cfg := Config{
		Model:          "nofb-1",
		MaxRetries:     1,
		FallbackModels: nil, // no fallback configured
	}

	_, err := Run(context.Background(), primary, d, &msgs, cfg)
	if err == nil {
		t.Fatal("expected error from primary")
	}
	if !strings.Contains(err.Error(), "non-retryable") {
		t.Errorf("expected 'non-retryable', got %v", err)
	}
}

func TestRunFallbackWithTools(t *testing.T) {
	// Primary fails, fallback succeeds and produces tools
	primary := &mockFailingProvider{name: "fb-tp", model: "tp-1", err: errors.New("down")}

	fallback := &mockToolProvider{
		events: []stream.Event{
			stream.ToolCallStart{ID: "tc1", Name: "read"},
			stream.ToolCallDelta{ID: "tc1", Delta: `{"path": "file.go"}`},
			stream.ToolCall{ID: "tc1", Name: "read", Arguments: `{"path": "file.go"}`},
			stream.Finish{Usage: stream.Usage{Input: 10, Output: 5}},
		},
	}

	providers.Register(fallback)

	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("read", "file content from fallback")
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "read file"}},
	}}

	cfg := Config{
		Model:          "tp-1",
		MaxRetries:     1,
		FallbackModels: []string{"mock-tool/mock-tool-1"},
		Tools:          runner,
	}

	_, err := Run(context.Background(), primary, d, &msgs, cfg)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Should have tool calls executed
	if len(d.toolCallStarts) == 0 {
		t.Fatal("expected tool calls from fallback")
	}
	if d.toolCallStarts[0] != "read" {
		t.Errorf("expected 'read', got %q", d.toolCallStarts[0])
	}
	if len(d.toolCallEnds) == 0 || d.toolCallEnds[0].Result != "file content from fallback" {
		t.Errorf("expected tool result 'file content from fallback', got %+v", d.toolCallEnds)
	}

	// Should have 3 messages: user + assistant + toolResult
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
}

func TestRunFallbackNoToolsTextOnly(t *testing.T) {
	// Primary fails, fallback succeeds with text only (no tools)
	primary := &mockFailingProvider{name: "fb-ntp", model: "ntp-1", err: errors.New("down")}

	fallback := &mockTextProvider{name: "fb-ntf", model: "ntf-1", chunks: []string{"just text, no tools"}}

	providers.Register(fallback)

	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "hello"}},
	}}

	cfg := Config{
		Model:          "ntp-1",
		MaxRetries:     1,
		FallbackModels: []string{"fb-ntf/ntf-1"},
	}

	_, err := Run(context.Background(), primary, d, &msgs, cfg)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	fullText := strings.Join(d.text, "")
	if !strings.Contains(fullText, "just text, no tools") {
		t.Errorf("expected fallback text, got %q", fullText)
	}
}

func TestRunFallbackUsedForRestOfSession(t *testing.T) {
	// After fallback, subsequent iterations use the fallback provider/model.
	// We simulate two iterations: first fails, fallback succeeds,
	// second iteration (no tools) should use the fallback.
	primary := &mockFailingProvider{name: "fb-sess", model: "sess-1", err: errors.New("down")}

	// The fallback returns text first, then on second call returns different text
	fallback := &mockToolProvider{
		events: []stream.Event{
			stream.TextDelta{Text: "first call "},
			stream.ToolCallStart{ID: "tc1", Name: "bash"},
			stream.ToolCallDelta{ID: "tc1", Delta: `{"command": "echo hello"}`},
			stream.ToolCall{ID: "tc1", Name: "bash", Arguments: `{"command": "echo hello"}`},
			stream.Finish{Usage: stream.Usage{Input: 5, Output: 3}},
		},
	}

	providers.Register(fallback)

	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("bash", "hello world")
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "run bash"}},
	}}

	cfg := Config{
		Model:          "sess-1",
		MaxRetries:     1,
		FallbackModels: []string{"mock-tool/mock-tool-1"},
		Tools:          runner,
	}

	_, err := Run(context.Background(), primary, d, &msgs, cfg)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// First iteration: fallback was used, tool executed
	if len(d.toolCallEnds) == 0 || d.toolCallEnds[0].Result != "hello world" {
		t.Errorf("expected tool result 'hello world', got %+v", d.toolCallEnds)
	}

	// The fallback provider was used (the text came from mockToolProvider)
	// which returned "first call " text
	if len(d.text) == 0 || !strings.Contains(strings.Join(d.text, ""), "first call") {
		t.Errorf("expected text from fallback, got %q", strings.Join(d.text, ""))
	}
}

func TestRunNoFallbackNormalPath(t *testing.T) {
	// No fallback configured — standard path should work fine
	p := &mockProvider{chunks: []string{"hello world"}}
	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	cfg := Config{
		Model:          "mock-1",
		MaxRetries:     1,
		FallbackModels: nil,
	}

	_, err := Run(context.Background(), p, d, &msgs, cfg)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if len(msgs[1].Content) == 0 || msgs[1].Content[0].Text != "hello world" {
		t.Errorf("expected 'hello world', got %+v", msgs[1].Content)
	}
}

// ─── Total() cost-summary emission tests ───────────────────────────────────
//
// The agent must always emit a final Total(...) call to the display so the
// user gets a cost summary even when the run ends via fallback, retry
// exhaustion, context cancellation, or non-retryable error. Previously
// several return paths skipped this call.

func assertTotalCalled(t *testing.T, d *captureDisplay) {
	t.Helper()
	if len(d.totals) == 0 {
		t.Fatal("expected at least one Total() call, got none")
	}
}

func TestRun_TotalCalledOnSuccess(t *testing.T) {
	p := &mockProvider{chunks: []string{"hello"}}
	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{Model: "mock-1", MaxRetries: 1}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	assertTotalCalled(t, d)
	if len(d.totals) != 1 {
		t.Errorf("expected exactly 1 Total() call on simple success, got %d", len(d.totals))
	}
}

func TestRun_TotalCalledOnFallbackSuccess(t *testing.T) {
	// Regression: primary provider fails, fallback succeeds. Previously the
	// `return totalUsage, nil` inside the `if !fbMore` branch skipped the
	// Total() call.
	primary := &mockFailingProvider{name: "fb-tot-p", model: "tot-p-1", err: errors.New("primary down")}
	fallback := &mockTextProvider{name: "fb-tot-fb", model: "tot-fb-1", chunks: []string{"fallback ok"}}
	providers.Register(fallback)

	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	_, err := Run(context.Background(), primary, d, &msgs, Config{
		Model:          "tot-p-1",
		MaxRetries:     1,
		FallbackModels: []string{"fb-tot-fb/tot-fb-1"},
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	assertTotalCalled(t, d)
}

func TestRun_TotalCalledOnAllFallbacksExhausted(t *testing.T) {
	// Primary fails, every fallback fails — we should still see Total() so
	// the user sees accumulated cost (likely 0, but the summary should be
	// emitted).
	primary := &mockFailingProvider{name: "fb-all-p", model: "all-p-1", err: errors.New("primary down")}
	fb1 := &mockFailingProvider{name: "fb-all-f1", model: "all-f1", err: errors.New("fb1 down")}
	fb2 := &mockFailingProvider{name: "fb-all-f2", model: "all-f2", err: errors.New("fb2 down")}
	providers.Register(fb1)
	providers.Register(fb2)

	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	_, err := Run(context.Background(), primary, d, &msgs, Config{
		Model:          "all-p-1",
		MaxRetries:     1,
		FallbackModels: []string{"fb-all-f1/all-f1", "fb-all-f2/all-f2"},
	})
	if err == nil {
		t.Fatal("expected error when all fallbacks fail")
	}

	assertTotalCalled(t, d)
}

func TestRun_TotalCalledOnContextCancel(t *testing.T) {
	// Stream returns context.Canceled, Run should still emit Total.
	cancelledProvider := &blockingProvider{name: "fb-ctx", model: "blocking-1"}

	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := Run(ctx, cancelledProvider, d, &msgs, Config{Model: "blocking-1", MaxRetries: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}

	assertTotalCalled(t, d)
}

func TestRun_TotalCalledOnNonRetryable(t *testing.T) {
	// Plain non-retryable error (e.g. plain errors.New) with no fallbacks.
	primary := &mockFailingProvider{name: "fb-nr", model: "nr-1", err: errors.New("fatal")}

	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	_, err := Run(context.Background(), primary, d, &msgs, Config{Model: "nr-1", MaxRetries: 3})
	if err == nil {
		t.Fatal("expected error from non-retryable failure")
	}

	assertTotalCalled(t, d)
}

func TestRun_TotalCalledOnAllRetriesExhausted(t *testing.T) {
	// Retryable error, primary provider always fails, no fallbacks, all
	// retries exhausted. We cancel ctx during the first backoff to avoid
	// waiting 4+8+16+... seconds in the test. io.EOF is recognised as
	// retryable by api.IsRetryable.
	primary := &mockFailingProvider{name: "fb-rx", model: "rx-1", err: io.EOF}

	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Poll the first ToolBlock("retry 1/5 ...") call to know the agent has
	// entered the backoff sleep, then cancel the context.
	gotBackoff := make(chan struct{})
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			d.mu.Lock()
			seen := false
			for _, tb := range d.toolBlocks {
				if strings.HasPrefix(tb, "retry 1/5") {
					seen = true
					break
				}
			}
			d.mu.Unlock()
			if seen {
				close(gotBackoff)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, primary, d, &msgs, Config{Model: "rx-1", MaxRetries: 5})
		done <- err
	}()

	select {
	case <-gotBackoff:
		cancel()
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("did not observe first retry backoff within 2s")
	}

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled during backoff, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s after cancel")
	}

	assertTotalCalled(t, d)
}

func TestRun_TotalCalledOnEveryToolTurn(t *testing.T) {
	// The user asked to see the cumulative cost after every tool turn
	// (not only at the very end). Total() must be emitted right after
	// the Summary of the tool-using iteration, with the current
	// totalUsage (which now includes the just-finished iteration's
	// tokens). The follow-up iteration is empty (no usage) so it
	// does not produce an extra Total().
	p := &mockToolProvider{
		events: []stream.Event{
			stream.TextDelta{Text: "first "},
			stream.ToolCallStart{ID: "tc1", Name: "bash"},
			stream.ToolCallDelta{ID: "tc1", Delta: `{"command": "echo hi"}`},
			stream.ToolCall{ID: "tc1", Name: "bash", Arguments: `{"command": "echo hi"}`},
			stream.Finish{Usage: stream.Usage{Input: 5, Output: 3}},
		},
	}
	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("bash", "hi")
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "echo"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "mock-tool-1",
		MaxRetries: 1,
		Tools:      runner,
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if got := len(d.totals); got != 1 {
		t.Fatalf("expected exactly 1 Total() call (one per tool turn), got %d", got)
	}
	// And the Total must reflect the cumulative usage (Input 5 from the
	// tool iteration, not 0 – that was the original bug).
	if d.totals[0].Input != 5 {
		t.Errorf("expected Total.Input=5 (cumulative), got %d", d.totals[0].Input)
	}
}

func TestRun_TotalNotDuplicatedOnFallbackSuccess(t *testing.T) {
	// Regression: when primary fails and fallback succeeds, the Costs
	// line was printed twice (once by runOnce inside tryFallback, once
	// by agent.Run before returning). User reported this as a duplicate
	// "Costs: in=88 out=43 cin=2304 cout=88" line.
	primary := &mockFailingProvider{name: "fb-dup-p", model: "dup-p-1", err: errors.New("rate limited")}
	fallback := &mockTextProvider{name: "fb-dup-fb", model: "dup-fb-1", chunks: []string{"hello"}}
	providers.Register(fallback)

	d := newCaptureDisplay()
	msgs := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	if _, err := Run(context.Background(), primary, d, &msgs, Config{
		Model:          "dup-p-1",
		MaxRetries:     1,
		FallbackModels: []string{"fb-dup-fb/dup-fb-1"},
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if got := len(d.totals); got != 1 {
		t.Fatalf("expected exactly 1 Total() call after fallback success, got %d", got)
	}
}

// blockingProvider blocks the stream until the context is cancelled, then
// returns ctx.Err(). It exercises the context.Canceled return path in
// agent.Run.
type blockingProvider struct {
	name  string
	model string
}

func (b *blockingProvider) Name() string         { return b.name }
func (b *blockingProvider) IsConfigured() bool   { return true }
func (b *blockingProvider) Models() []string     { return []string{b.model} }
func (b *blockingProvider) FreeModels() []string { return nil }

func (b *blockingProvider) Stream(ctx context.Context, req providers.Request) (<-chan stream.Event, error) {
	ch := make(chan stream.Event, 1)
	go func() {
		defer close(ch)
		<-ctx.Done()
		// No usage reported on cancel — totalUsage stays at zero.
		ch <- stream.StreamError{Err: ctx.Err()}
	}()
	return ch, nil
}
