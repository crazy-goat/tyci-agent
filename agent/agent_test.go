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

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
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

// The model doubles in this package are connectortest.Fake literals. Two
// things about them are load-bearing and therefore spelled out at every call
// site rather than hidden behind a helper:
//
//   - the Usage on the final Finish. runOnce only emits Summary/Total when
//     hasUsage(lastUsage) is true, so a zero Usage is not a cosmetic
//     difference — it decides whether a Costs line appears at all.
//   - what happens after the script runs out. Fake's default (OnExhausted
//     nil) is Finish{Reason: "stop"} with zero usage, which is exactly what
//     the old mockToolProvider did on its second and later calls; a double
//     that must instead close the channel with no Finish says so with
//     OnExhausted: []stream.Event{}.
//
// Finish.Reason itself is not load-bearing: runOnce reads only e.Usage.

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
	p := &connectortest.Fake{ProviderName: "mock", ModelName: "mock-1", Turns: [][]stream.Event{{
		stream.TextDelta{Text: "Hello"},
		stream.TextDelta{Text: " world"},
		stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
	}}}
	d := &silentDisplay{}
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "Hi"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{MaxRetries: 1}); err != nil {
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
	p := &connectortest.Fake{ProviderName: "mock", ModelName: "mock-1", Turns: [][]stream.Event{{
		stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
	}}}
	d := &silentDisplay{}
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "Hi"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{MaxRetries: 1}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %#v", len(msgs), msgs)
	}
}

func TestRun_ToolCall_ShowsToolBlockDuringStream(t *testing.T) {
	p := &connectortest.Fake{
		ProviderName: "mock-tool",
		ModelName:    "mock-tool-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "I'll look that up."},
			stream.ToolCallStart{ID: "tc1", Name: "read"},
			stream.ToolCallDelta{ID: "tc1", Delta: `{"path": "file.go"}`},
			stream.ToolCall{ID: "tc1", Name: "read", Arguments: `{"path": "file.go"}`},
			stream.Finish{Usage: stream.Usage{Input: 10, Output: 5}},
		}},
	}
	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("read", "file content")
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "read file.go"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
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
	p := &connectortest.Fake{
		ProviderName: "mock-tool",
		ModelName:    "mock-tool-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "No tools needed."},
			stream.Finish{Usage: stream.Usage{Input: 5, Output: 3}},
		}},
	}
	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "Hello"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
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
	p := &connectortest.Fake{
		ProviderName: "mock-tool",
		ModelName:    "mock-tool-1",
		Turns: [][]stream.Event{{
			stream.ToolCallStart{ID: "tc1", Name: "read"},
			stream.ToolCallDelta{ID: "tc1", Delta: `{"path": "a.go"}`},
			stream.ToolCall{ID: "tc1", Name: "read", Arguments: `{"path": "a.go"}`},
			stream.ToolCallStart{ID: "tc2", Name: "bash"},
			stream.ToolCallDelta{ID: "tc2", Delta: `{"command": "ls"}`},
			stream.ToolCall{ID: "tc2", Name: "bash", Arguments: `{"command": "ls"}`},
			stream.Finish{Usage: stream.Usage{Input: 20, Output: 10}},
		}},
	}
	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("read", "content of a.go")
	runner.SetResult("bash", "file1\nfile2")
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "list and read"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
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
	p := &connectortest.Fake{
		ProviderName: "mock-tool",
		ModelName:    "mock-tool-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "I'll check that file. "},
			stream.ToolCallStart{ID: "tc1", Name: "read"},
			stream.ToolCallDelta{ID: "tc1", Delta: `{"path": "x.go"}`},
			stream.ToolCall{ID: "tc1", Name: "read", Arguments: `{"path": "x.go"}`},
			stream.Finish{Usage: stream.Usage{Input: 15, Output: 7}},
		}},
	}
	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("read", "package main")
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "read x.go"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
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
	p := &connectortest.Fake{
		ProviderName: "mock-tool",
		ModelName:    "mock-tool-1",
		Turns: [][]stream.Event{{
			stream.ToolCall{ID: "tc1", Name: "bash", Arguments: `{"command": "echo hi"}`},
			stream.Finish{Usage: stream.Usage{Input: 5, Output: 2}},
		}},
	}
	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("bash", "hi")
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "echo"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
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
	p := &connectortest.Fake{
		ProviderName: "mock-tool",
		ModelName:    "mock-tool-1",
		Turns: [][]stream.Event{{
			stream.ToolCallStart{ID: "tc1", Name: "bash"},
			stream.ToolCall{ID: "tc1", Name: "bash", Arguments: `{"command": "false"}`},
			stream.Finish{Usage: stream.Usage{Input: 5, Output: 2}},
		}},
	}
	d := newCaptureDisplay()
	runner := newMockToolRunner()
	// No result set → Run returns empty string, no error
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "run"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
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
	p := &connectortest.Fake{
		ProviderName: "mock-tool",
		ModelName:    "mock-tool-1",
		Turns: [][]stream.Event{{
			stream.StreamError{Err: errors.New("connection lost")},
		}},
	}
	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	_, err := Run(context.Background(), p, d, &msgs, Config{
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

func TestRunFallbackPrimaryFailsFallbackSucceeds(t *testing.T) {
	// Primary provider returns error, fallback succeeds with text
	primary := &connectortest.Fake{ProviderName: "fb-primary", ModelName: "primary-1", StreamErr: errors.New("server error 500")}

	fallback := &connectortest.Fake{ProviderName: "fb-fallback", ModelName: "fb-1", Turns: [][]stream.Event{{
		stream.TextDelta{Text: "fallback response"},
		stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
	}}}

	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "Hello"}},
	}}

	cfg := Config{
		MaxRetries: 1,
		Fallbacks:  []connector.ModelClient{fallback},
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
	primary := &connectortest.Fake{ProviderName: "fb-p1", ModelName: "p1", StreamErr: errors.New("primary dead")}

	fallback1 := &connectortest.Fake{ProviderName: "fb-f1", ModelName: "f1", StreamErr: errors.New("fb1 dead")}
	fallback2 := &connectortest.Fake{ProviderName: "fb-f2", ModelName: "f2", StreamErr: errors.New("fb2 dead")}

	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "Hello"}},
	}}

	cfg := Config{
		MaxRetries: 1,
		Fallbacks:  []connector.ModelClient{fallback1, fallback2},
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
	primary := &connectortest.Fake{ProviderName: "fb-nofb", ModelName: "nofb-1", StreamErr: errors.New("non-retryable")}

	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "Hello"}},
	}}

	cfg := Config{
		MaxRetries: 1,
		Fallbacks:  nil, // no fallback configured
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
	primary := &connectortest.Fake{ProviderName: "fb-tp", ModelName: "tp-1", StreamErr: errors.New("down")}

	fallback := &connectortest.Fake{
		ProviderName: "mock-tool",
		ModelName:    "mock-tool-1",
		Turns: [][]stream.Event{{
			stream.ToolCallStart{ID: "tc1", Name: "read"},
			stream.ToolCallDelta{ID: "tc1", Delta: `{"path": "file.go"}`},
			stream.ToolCall{ID: "tc1", Name: "read", Arguments: `{"path": "file.go"}`},
			stream.Finish{Usage: stream.Usage{Input: 10, Output: 5}},
		}},
	}

	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("read", "file content from fallback")
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "read file"}},
	}}

	cfg := Config{
		MaxRetries: 1,
		Fallbacks:  []connector.ModelClient{fallback},
		Tools:      runner,
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
	primary := &connectortest.Fake{ProviderName: "fb-ntp", ModelName: "ntp-1", StreamErr: errors.New("down")}

	fallback := &connectortest.Fake{ProviderName: "fb-ntf", ModelName: "ntf-1", Turns: [][]stream.Event{{
		stream.TextDelta{Text: "just text, no tools"},
		stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
	}}}

	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hello"}},
	}}

	cfg := Config{
		MaxRetries: 1,
		Fallbacks:  []connector.ModelClient{fallback},
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

// TestRunFallback_MidStreamFailureAfterPartialText covers the failure shape no
// hand-written double in this package could produce: the request succeeds, the
// model starts answering, and the stream dies half-way through. Until
// connectortest.Flaky every fallback test here failed at Stream() itself, so
// the "partial answer already on screen, then switch models" path — a real
// production path — was untested.
//
// The injected error is deliberately NOT retryable, because this test is about
// the fallback path: a retryable one would send Run into the retry loop first.
// That loop's backoff comes from api.CalcBackoff with a BaseBackoff that
// defaults to 4 and cannot be injected — four seconds minimum per attempt,
// unless the error happens to be a 429 carrying Retry-After (which is the
// narrow door TestRun_RetryRecoversAfterMidStreamRateLimit goes through).
// Non-retryable goes straight to the fallback with no sleep at all.
// (See the debt note on the un-injectable backoff in docs/architecture-refactor.md.)
func TestRunFallback_MidStreamFailureAfterPartialText(t *testing.T) {
	// Two TextDeltas reach the display, then the stream dies. The third
	// chunk and the Finish of the wrapped Fake are never reached, which is
	// why Text()'s zero Usage does not matter here.
	primary := &connectortest.Flaky{
		Client: connectortest.Text("Here is the ", "first half of ", "the answer."),
		Failures: []connectortest.Failure{{
			MidStream:   true,
			AfterEvents: 2,
			Err:         errors.New("connection reset mid-stream"),
		}},
	}

	fallback := &connectortest.Fake{ProviderName: "fb-mid", ModelName: "mid-1", Turns: [][]stream.Event{{
		stream.TextDelta{Text: "complete answer from the fallback"},
		stream.Finish{Usage: stream.Usage{Input: 7, Output: 4}},
	}}}

	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	if _, err := Run(context.Background(), primary, d, &msgs, Config{
		MaxRetries: 1,
		Fallbacks:  []connector.ModelClient{fallback},
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// The primary was called exactly once — the failure sent Run to the
	// fallback, not back to the primary.
	if got := primary.Calls(); got != 1 {
		t.Errorf("primary Stream calls = %d, want 1", got)
	}

	// The half-answer really did reach the display before the failure...
	shown := strings.Join(d.text, "")
	if !strings.Contains(shown, "Here is the first half of ") {
		t.Errorf("expected the partial primary answer on screen, got %q", shown)
	}
	// ...and the fallback's answer followed it.
	if !strings.Contains(shown, "complete answer from the fallback") {
		t.Errorf("expected the fallback answer on screen, got %q", shown)
	}

	// The truncated turn must NOT be recorded as an assistant message:
	// runOnce bails on StreamError before appending, so the conversation
	// carries only the fallback's complete answer.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (user, assistant), got %d: %#v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" {
		t.Fatalf("expected assistant message, got %q", msgs[1].Role)
	}
	if msgs[1].Content[0].Text != "complete answer from the fallback" {
		t.Errorf("assistant text = %q, want only the fallback's answer",
			msgs[1].Content[0].Text)
	}

	// The user still gets exactly one cost summary, carrying the fallback's
	// usage — the dead primary reported none.
	if got := len(d.totals); got != 1 {
		t.Fatalf("expected exactly 1 Total() call, got %d", got)
	}
	if d.totals[0].Input != 7 || d.totals[0].Output != 4 {
		t.Errorf("Total = %+v, want Input 7 / Output 4 from the fallback", d.totals[0])
	}
}

// TestRun_RetryRecoversAfterMidStreamRateLimit covers the retry loop's success
// case, which nothing covered before: the only retry test in this package
// (TestRun_TotalCalledOnAllRetriesExhausted) cancels the context during the
// first backoff and therefore never reaches a recovery.
//
// It runs in milliseconds despite the un-injectable backoff, and the reason is
// worth writing down: api.CalcBackoff honours a 429's Retry-After header
// verbatim, so RateLimited("0") asks for a zero-second wait and
// sleepWithCountdown returns immediately. That is a real provider behaviour,
// not a test hack — but it is also the ONLY way in, and it does not help the
// 500/EOF cases, which always take BaseBackoff's four seconds. See the debt
// note on the un-injectable backoff in docs/architecture-refactor.md.
func TestRun_RetryRecoversAfterMidStreamRateLimit(t *testing.T) {
	primary := &connectortest.Flaky{
		Client: &connectortest.Fake{ProviderName: "rl", ModelName: "rl-1", Turns: [][]stream.Event{
			{
				stream.TextDelta{Text: "starting to answ"},
				stream.TextDelta{Text: "er when the 429 lands"},
				stream.Finish{Usage: stream.Usage{Input: 3, Output: 2}},
			},
			{
				stream.TextDelta{Text: "the retried answer"},
				stream.Finish{Usage: stream.Usage{Input: 9, Output: 6}},
			},
		}},
		// Only the first call fails; the second passes through untouched.
		Failures: []connectortest.Failure{{
			MidStream:   true,
			AfterEvents: 1,
			Err:         connectortest.RateLimited("0"),
		}},
	}

	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	if _, err := Run(context.Background(), primary, d, &msgs, Config{MaxRetries: 3}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if got := primary.Calls(); got != 2 {
		t.Errorf("Stream calls = %d, want 2 (one failure, one retry)", got)
	}

	// The retry really went through the retry loop, not the fallback path.
	sawRetry := false
	for _, tb := range d.toolBlocks {
		if strings.HasPrefix(tb, "retry 1/3") {
			sawRetry = true
		}
	}
	if !sawRetry {
		t.Errorf("expected a retry notice on the display, got %v", d.toolBlocks)
	}

	// The truncated first attempt left its half-sentence on screen but must
	// not be recorded: only the retried turn becomes an assistant message.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (user, assistant), got %d: %#v", len(msgs), msgs)
	}
	if msgs[1].Content[0].Text != "the retried answer" {
		t.Errorf("assistant text = %q, want only the retried answer", msgs[1].Content[0].Text)
	}

	// Usage is the retried turn's alone — the abandoned attempt never
	// reached its Finish, so its 3/2 must not be counted.
	if got := len(d.totals); got != 1 {
		t.Fatalf("expected exactly 1 Total() call, got %d", got)
	}
	if d.totals[0].Input != 9 || d.totals[0].Output != 6 {
		t.Errorf("Total = %+v, want Input 9 / Output 6 from the retried turn", d.totals[0])
	}
}

func TestRunFallbackUsedForRestOfSession(t *testing.T) {
	// After fallback, subsequent iterations use the fallback provider/model.
	// We simulate two iterations: first fails, fallback succeeds,
	// second iteration (no tools) should use the fallback.
	primary := &connectortest.Fake{ProviderName: "fb-sess", ModelName: "sess-1", StreamErr: errors.New("down")}

	// The fallback returns text first, then on second call returns different text
	fallback := &connectortest.Fake{
		ProviderName: "mock-tool",
		ModelName:    "mock-tool-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "first call "},
			stream.ToolCallStart{ID: "tc1", Name: "bash"},
			stream.ToolCallDelta{ID: "tc1", Delta: `{"command": "echo hello"}`},
			stream.ToolCall{ID: "tc1", Name: "bash", Arguments: `{"command": "echo hello"}`},
			stream.Finish{Usage: stream.Usage{Input: 5, Output: 3}},
		}},
	}

	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("bash", "hello world")
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "run bash"}},
	}}

	cfg := Config{
		MaxRetries: 1,
		Fallbacks:  []connector.ModelClient{fallback},
		Tools:      runner,
	}

	_, err := Run(context.Background(), primary, d, &msgs, cfg)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// First iteration: fallback was used, tool executed
	if len(d.toolCallEnds) == 0 || d.toolCallEnds[0].Result != "hello world" {
		t.Errorf("expected tool result 'hello world', got %+v", d.toolCallEnds)
	}

	// The fallback provider was used: its first scripted turn returned
	// the "first call " text.
	if len(d.text) == 0 || !strings.Contains(strings.Join(d.text, ""), "first call") {
		t.Errorf("expected text from fallback, got %q", strings.Join(d.text, ""))
	}
}

func TestRunNoFallbackNormalPath(t *testing.T) {
	// No fallback configured — standard path should work fine
	p := &connectortest.Fake{ProviderName: "mock", ModelName: "mock-1", Turns: [][]stream.Event{{
		stream.TextDelta{Text: "hello world"},
		stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
	}}}
	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	cfg := Config{
		MaxRetries: 1,
		Fallbacks:  nil,
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
	p := &connectortest.Fake{ProviderName: "mock", ModelName: "mock-1", Turns: [][]stream.Event{{
		stream.TextDelta{Text: "hello"},
		stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
	}}}
	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{MaxRetries: 1}); err != nil {
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
	primary := &connectortest.Fake{ProviderName: "fb-tot-p", ModelName: "tot-p-1", StreamErr: errors.New("primary down")}
	fallback := &connectortest.Fake{ProviderName: "fb-tot-fb", ModelName: "tot-fb-1", Turns: [][]stream.Event{{
		stream.TextDelta{Text: "fallback ok"},
		stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
	}}}

	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	_, err := Run(context.Background(), primary, d, &msgs, Config{
		MaxRetries: 1,
		Fallbacks:  []connector.ModelClient{fallback},
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
	primary := &connectortest.Fake{ProviderName: "fb-all-p", ModelName: "all-p-1", StreamErr: errors.New("primary down")}
	fb1 := &connectortest.Fake{ProviderName: "fb-all-f1", ModelName: "all-f1", StreamErr: errors.New("fb1 down")}
	fb2 := &connectortest.Fake{ProviderName: "fb-all-f2", ModelName: "all-f2", StreamErr: errors.New("fb2 down")}

	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	_, err := Run(context.Background(), primary, d, &msgs, Config{
		MaxRetries: 1,
		Fallbacks:  []connector.ModelClient{fb1, fb2},
	})
	if err == nil {
		t.Fatal("expected error when all fallbacks fail")
	}

	assertTotalCalled(t, d)
}

func TestRun_TotalCalledOnContextCancel(t *testing.T) {
	// Stream returns context.Canceled, Run should still emit Total.
	cancelledProvider := &connectortest.Fake{ProviderName: "fb-ctx", ModelName: "blocking-1", BlockUntilCancel: true}

	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := Run(ctx, cancelledProvider, d, &msgs, Config{MaxRetries: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}

	assertTotalCalled(t, d)
}

func TestRun_TotalCalledOnNonRetryable(t *testing.T) {
	// Plain non-retryable error (e.g. plain errors.New) with no fallbacks.
	primary := &connectortest.Fake{ProviderName: "fb-nr", ModelName: "nr-1", StreamErr: errors.New("fatal")}

	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	_, err := Run(context.Background(), primary, d, &msgs, Config{MaxRetries: 3})
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
	primary := &connectortest.Fake{ProviderName: "fb-rx", ModelName: "rx-1", StreamErr: io.EOF}

	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
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
		_, err := Run(ctx, primary, d, &msgs, Config{MaxRetries: 5})
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
	p := &connectortest.Fake{
		ProviderName: "mock-tool",
		ModelName:    "mock-tool-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "first "},
			stream.ToolCallStart{ID: "tc1", Name: "bash"},
			stream.ToolCallDelta{ID: "tc1", Delta: `{"command": "echo hi"}`},
			stream.ToolCall{ID: "tc1", Name: "bash", Arguments: `{"command": "echo hi"}`},
			stream.Finish{Usage: stream.Usage{Input: 5, Output: 3}},
		}},
	}
	d := newCaptureDisplay()
	runner := newMockToolRunner()
	runner.SetResult("bash", "hi")
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "echo"}},
	}}

	if _, err := Run(context.Background(), p, d, &msgs, Config{
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
	primary := &connectortest.Fake{ProviderName: "fb-dup-p", ModelName: "dup-p-1", StreamErr: errors.New("rate limited")}
	fallback := &connectortest.Fake{ProviderName: "fb-dup-fb", ModelName: "dup-fb-1", Turns: [][]stream.Event{{
		stream.TextDelta{Text: "hello"},
		stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
	}}}

	d := newCaptureDisplay()
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	if _, err := Run(context.Background(), primary, d, &msgs, Config{
		MaxRetries: 1,
		Fallbacks:  []connector.ModelClient{fallback},
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if got := len(d.totals); got != 1 {
		t.Fatalf("expected exactly 1 Total() call after fallback success, got %d", got)
	}
}
