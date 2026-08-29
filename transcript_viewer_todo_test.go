package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/tools"
)

func resetTodoStoreForTranscriptTest(t *testing.T) {
	t.Helper()
	// tools has no exported reset; do it via tool clear + direct map wipe
	// Use the same technique as tools/todo_test.go resetTodoStoreForTest but
	// we are in package main so we must access via exported API.
	// Instead, snapshot and restore the todoStore around the test by using
	// the per-agent lists we create: we clean them up via clearing.
	// Simplest: use tools API to clear what we create, and rely on unique ids.
	_ = context.Background()
}

func seedTodosForAgent(t *testing.T, agentID string, contents []string) {
	t.Helper()
	tool := &tools.TodoTool{}
	ctx := context.WithValue(context.Background(), tools.TodoAgentCtxKey{}, agentID)
	for _, c := range contents {
		res := tool.Run(ctx, map[string]any{"action": "add", "content": c})
		if !res.Success {
			t.Fatalf("seed add %q for %q failed: %s", c, agentID, res.Error)
		}
	}
}

// TestTranscriptProvider_TodoResolved verifies the core item-42 behavior:
// a child's todo tool_call line renders with resolved item text (not raw JSON)
// via the per-agent todo store keyed by resumableEntry.todoAgentID.
// This test FAILS when the viewer falls back to raw args.
func TestTranscriptProvider_TodoResolved(t *testing.T) {
	resetResumableForTest(t)
	agentID := "todo-agent-resolved-42"
	seedTodosForAgent(t, agentID, []string{"Write integration tests", "Fix flaky test"})
	// Mark done so the list is considered terminal but NOT evicted in this test
	t.Cleanup(func() {
		// Clear seeded data by clearing via ctx
		tool := &tools.TodoTool{}
		ctx := context.WithValue(context.Background(), tools.TodoAgentCtxKey{}, agentID)
		tool.Run(ctx, map[string]any{"action": "clear"})
		tools.MarkTodoAgentDone(agentID)
	})

	jobID := "job-todo-resolved"
	msgs := []connector.Message{
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "toolCall", Name: "todo", Arguments: json.RawMessage(`{"action":"doing","id":1}`)}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "toolCall", Name: "todo", Arguments: json.RawMessage(`{"action":"add","content":"Write integration tests"}`)}}},
	}
	stashResumable(jobID, resumableEntry{msgs: msgs, todoAgentID: agentID})

	_, lines, ok := buildTranscriptProvider()(jobID)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %v", len(lines), lines)
	}
	// First line: todo(doing, 1. Write integration tests) or truncated variant — must contain resolved text
	if !strings.Contains(lines[0], "Write integration tests") {
		t.Fatalf("expected resolved todo content in line[0], got %q", lines[0])
	}
	if strings.Contains(lines[0], `"action":"doing"`) {
		t.Fatalf("line should be resolved, not raw JSON: %q", lines[0])
	}
	// Second line: todo(add: Write integration tests)
	if !strings.Contains(lines[1], "Write integration tests") {
		t.Fatalf("expected add content in line[1], got %q", lines[1])
	}
}

// TestTranscriptProvider_TodoFallbackUnknownAgent verifies that when
// todoAgentID has no store entry (unknown/evicted) the viewer falls back
// to raw args exactly as before — never panics, never drops the line.
func TestTranscriptProvider_TodoFallbackUnknownAgent(t *testing.T) {
	resetResumableForTest(t)
	jobID := "job-todo-fallback"
	raw := `{"action":"doing","id":7}`
	msgs := []connector.Message{
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "toolCall", Name: "todo", Arguments: json.RawMessage(raw)}}},
	}
	stashResumable(jobID, resumableEntry{msgs: msgs, todoAgentID: "no-such-agent-xyz"})

	_, lines, ok := buildTranscriptProvider()(jobID)
	if !ok {
		t.Fatalf("expected ok")
	}
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	// Fallback should render raw args (today's behavior) — resolved form would be "doing, 7"
	// Raw path includes the JSON; ensure it still contains the id
	if !strings.Contains(lines[0], `"action":"doing"`) || !strings.Contains(lines[0], `"id":7`) {
		t.Fatalf("fallback should be raw JSON, got %q", lines[0])
	}
}

// TestTranscriptProvider_TodoLongContentTruncated verifies that resolved
// content longer than the block cap still gets the truncation marker (runes,
// post-ANSI-strip rules unchanged).
func TestTranscriptProvider_TodoLongContentTruncated(t *testing.T) {
	resetResumableForTest(t)
	agentID := "todo-agent-long-42"
	long := strings.Repeat("漢", 5000) // CJK to verify rune-based cap
	seedTodosForAgent(t, agentID, []string{long})
	t.Cleanup(func() {
		tool := &tools.TodoTool{}
		ctx := context.WithValue(context.Background(), tools.TodoAgentCtxKey{}, agentID)
		tool.Run(ctx, map[string]any{"action": "clear"})
		tools.MarkTodoAgentDone(agentID)
	})
	jobID := "job-todo-long"
	msgs := []connector.Message{
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "toolCall", Name: "todo", Arguments: json.RawMessage(`{"action":"doing","id":1}`)}}},
	}
	stashResumable(jobID, resumableEntry{msgs: msgs, todoAgentID: agentID})

	_, lines, ok := buildTranscriptProvider()(jobID)
	if !ok {
		t.Fatalf("expected ok")
	}
	if len(lines) != 1 {
		t.Fatalf("want 1 line")
	}
	line := lines[0]
	if !strings.Contains(line, "...") {
		snippet := line
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		t.Fatalf("expected '...' in resolved long content, got %q", snippet)
	}
	// Resolved content truncated to 40 via truncateForTranscript
	// Ensure the line is bounded (not 5000 runes of CJK)
	if len([]rune(line)) > 2500 {
		t.Fatalf("line should be truncated, len runes=%d", len([]rune(line)))
	}
}

// TestTranscriptProvider_NonTodoUnchanged verifies regression: non-todo
// tool calls render exactly as before (raw args).
func TestTranscriptProvider_NonTodoUnchanged(t *testing.T) {
	resetResumableForTest(t)
	jobID := "job-nontodo"
	args := json.RawMessage(`{"path":"main.go"}`)
	msgs := []connector.Message{
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "toolCall", Name: "read", Arguments: args}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "toolCall", Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi"}`)}}},
	}
	stashResumable(jobID, resumableEntry{msgs: msgs, todoAgentID: "some-agent"})

	_, lines, ok := buildTranscriptProvider()(jobID)
	if !ok {
		t.Fatalf("expected ok")
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %v", lines)
	}
	if !strings.Contains(lines[0], `"path":"main.go"`) {
		t.Fatalf("non-todo read should be raw, got %q", lines[0])
	}
	if !strings.Contains(lines[1], `"command":"echo hi"`) {
		t.Fatalf("non-todo bash should be raw, got %q", lines[1])
	}
}
