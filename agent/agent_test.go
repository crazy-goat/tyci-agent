package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/decodo/tyci-agent/display"
	"github.com/decodo/tyci-agent/providers"
	"github.com/decodo/tyci-agent/session"
	"github.com/decodo/tyci-agent/stream"
)

type mockProvider struct {
	chunks []string
}

func (m *mockProvider) Name() string  { return "mock" }
func (m *mockProvider) IsConfigured() bool { return true }
func (m *mockProvider) Models() []string  { return []string{"mock-1"} }
func (m *mockProvider) FreeModels() []string { return nil }

func (m *mockProvider) Stream(ctx context.Context, req providers.Request) (<-chan stream.Event, error) {
	ch := make(chan stream.Event, len(m.chunks)+2)
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
	mu       sync.Mutex
	events   []stream.Event
	called   bool
}

func (m *mockToolProvider) Name() string  { return "mock-tool" }
func (m *mockToolProvider) IsConfigured() bool { return true }
func (m *mockToolProvider) Models() []string  { return []string{"mock-tool-1"} }
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

	ch := make(chan stream.Event, len(events)+2)
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
	mu       sync.Mutex
	calls    []toolCallRecord
	results  map[string]string
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

// captureDisplay implements display.Display and records all calls for assertion.
type captureDisplay struct {
	mu             sync.Mutex
	thinking       []string
	text           []string
	toolCallStarts []string
	toolCallDeltas []string
	toolCallEnds   []struct{ Name, Result string }
	toolBlocks     []string
	summaries      []stream.Usage
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

func (c *captureDisplay) Error(err error) {}
func (c *captureDisplay) End()            {}

func TestRunAppendsAssistantMessage(t *testing.T) {
	p := &mockProvider{chunks: []string{"Hello", " world"}}
	d := display.NewSilent()
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
	d := display.NewSilent()
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
