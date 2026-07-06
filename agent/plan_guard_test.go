package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
)

// ---------------------------------------------------------------------------
// Unit tests for enforcePlanGuard
// ---------------------------------------------------------------------------

func TestEnforcePlanGuard_DisabledWhenNil(t *testing.T) {
	cfg := Config{HasTodos: nil}
	calls := []stream.ToolCall{
		{Name: "read", ID: "1", Arguments: `{"path":"/tmp/f"}`},
	}
	toExec, idx, results := enforcePlanGuard(cfg, calls)
	if len(toExec) != 1 {
		t.Fatalf("expected 1 call to execute, got %d", len(toExec))
	}
	if idx != nil {
		t.Fatalf("expected nil origIdx, got %v", idx)
	}
	if results != nil {
		t.Fatalf("expected nil results, got %v", results)
	}
}

func TestEnforcePlanGuard_PassWhenTodosExist(t *testing.T) {
	cfg := Config{HasTodos: func() bool { return true }}
	calls := []stream.ToolCall{
		{Name: "read", ID: "1", Arguments: `{"path":"/tmp/f"}`},
		{Name: "bash", ID: "2", Arguments: `{"command":"ls"}`},
	}
	toExec, idx, results := enforcePlanGuard(cfg, calls)
	if len(toExec) != 2 {
		t.Fatalf("expected 2 calls to execute, got %d", len(toExec))
	}
	if idx != nil {
		t.Fatalf("expected nil origIdx, got %v", idx)
	}
	if results != nil {
		t.Fatalf("expected nil results, got %v", results)
	}
}

func TestEnforcePlanGuard_BlocksNonTodoTools(t *testing.T) {
	cfg := Config{HasTodos: func() bool { return false }}
	calls := []stream.ToolCall{
		{Name: "read", ID: "1", Arguments: `{"path":"/tmp/f"}`},
		{Name: "bash", ID: "2", Arguments: `{"command":"ls"}`},
	}
	toExec, _, results := enforcePlanGuard(cfg, calls)
	if len(toExec) != 0 {
		t.Fatalf("expected 0 calls to execute, got %d", len(toExec))
	}
	for i, r := range results {
		if !strings.Contains(r, "todo tool") {
			t.Errorf("results[%d] = %q, want it to mention the todo tool", i, r)
		}
	}
}

func TestEnforcePlanGuard_AllowsTodoTool(t *testing.T) {
	cfg := Config{HasTodos: func() bool { return false }}
	calls := []stream.ToolCall{
		{Name: "todo", ID: "1", Arguments: `{"action":"add","content":"plan"}`},
	}
	toExec, idx, results := enforcePlanGuard(cfg, calls)
	if len(toExec) != 1 {
		t.Fatalf("expected 1 call to execute, got %d", len(toExec))
	}
	if toExec[0].Name != "todo" {
		t.Errorf("expected todo call, got %q", toExec[0].Name)
	}
	if len(idx) != 1 || idx[0] != 0 {
		t.Errorf("expected origIdx=[0], got %v", idx)
	}
	// results[0] should be empty string (not pre-filled with error)
	if results[0] != "" {
		t.Errorf("expected empty result for todo call, got %q", results[0])
	}
}

func TestEnforcePlanGuard_MixedBatch(t *testing.T) {
	cfg := Config{HasTodos: func() bool { return false }}
	calls := []stream.ToolCall{
		{Name: "read", ID: "1", Arguments: `{"path":"/tmp/f"}`},
		{Name: "todo", ID: "2", Arguments: `{"action":"add","content":"plan"}`},
		{Name: "bash", ID: "3", Arguments: `{"command":"ls"}`},
	}
	toExec, idx, results := enforcePlanGuard(cfg, calls)
	if len(toExec) != 1 {
		t.Fatalf("expected 1 call to execute, got %d", len(toExec))
	}
	if toExec[0].Name != "todo" {
		t.Errorf("expected todo call, got %q", toExec[0].Name)
	}
	if len(idx) != 1 || idx[0] != 1 {
		t.Errorf("expected origIdx=[1], got %v", idx)
	}
	// results[0] and results[2] should have errors
	if !strings.Contains(results[0], "todo tool") {
		t.Errorf("results[0] should be blocked: %q", results[0])
	}
	if results[1] != "" {
		t.Errorf("results[1] should be empty (todo call): %q", results[1])
	}
	if !strings.Contains(results[2], "todo tool") {
		t.Errorf("results[2] should be blocked: %q", results[2])
	}
}

// ---------------------------------------------------------------------------
// Integration tests with agent.Run
// ---------------------------------------------------------------------------

// planGuardProvider emits a bash tool call on the first invocation, then
// finishes. Used to test that the guard blocks non-todo tools.
type planGuardProvider struct {
	mu     sync.Mutex
	called bool
}

func (p *planGuardProvider) Name() string         { return "pg" }
func (p *planGuardProvider) IsConfigured() bool   { return true }
func (p *planGuardProvider) Models() []string     { return []string{"pg-1"} }
func (p *planGuardProvider) FreeModels() []string { return nil }

func (p *planGuardProvider) Stream(ctx context.Context, req providers.Request) (<-chan stream.Event, error) {
	p.mu.Lock()
	first := !p.called
	p.called = true
	p.mu.Unlock()

	ch := make(chan stream.Event, 4)
	if first {
		// Emit a bash tool call.
		go func() {
			defer close(ch)
			ch <- stream.ToolCallStart{Name: "bash"}
			ch <- stream.ToolCallDelta{Delta: `{"command":"echo hello"}`}
			ch <- stream.ToolCall{ID: "tc1", Name: "bash", Arguments: `{"command":"echo hello"}`}
			ch <- stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}}
		}()
	} else {
		// No tool calls — just finish.
		close(ch)
	}
	return ch, nil
}

// TestRun_PlanGuard_BlocksBashWithoutPlan verifies that when HasTodos
// returns false, a bash tool call gets an error result telling the LLM
// to create a plan first. The LLM sees the error and (in real usage)
// would switch to using the todo tool.
func TestRun_PlanGuard_BlocksBashWithoutPlan(t *testing.T) {
	p := &planGuardProvider{}
	d := &captureDisplay{}
	msgs := []providers.RichMessage{
		{Role: "user", Content: []providers.ContentBlock{{Type: "text", Text: "do something"}}},
	}

	_, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "pg-1",
		MaxRetries: 1,
		HasTodos:   func() bool { return false },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The tool call should have been intercepted — check that the result
	// shown to the model contains the plan-required error.
	found := false
	for _, end := range d.toolCallEnds {
		if end.Name == "bash" && strings.Contains(end.Result, "todo tool") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected bash tool call to be blocked with plan-required error")
		// Dump what we got for debugging.
		for _, end := range d.toolCallEnds {
			t.Logf("  toolCallEnd: name=%q result=%q", end.Name, end.Result)
		}
	}

	// The tool result message should be marked as an error.
	for _, m := range msgs {
		if m.Role == "toolResult" && len(m.Content) > 0 {
			if m.Content[0].ToolName == "bash" && !m.Content[0].IsError {
				t.Error("expected bash tool result to be marked as IsError")
			}
		}
	}
}

// planGuardTodoProvider emits a todo tool call on the first invocation
// (creating a plan), then a bash tool call on the second, then finishes.
type planGuardTodoProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *planGuardTodoProvider) Name() string         { return "pgt" }
func (p *planGuardTodoProvider) IsConfigured() bool   { return true }
func (p *planGuardTodoProvider) Models() []string     { return []string{"pgt-1"} }
func (p *planGuardTodoProvider) FreeModels() []string { return nil }

func (p *planGuardTodoProvider) Stream(ctx context.Context, req providers.Request) (<-chan stream.Event, error) {
	p.mu.Lock()
	call := p.calls
	p.calls++
	p.mu.Unlock()

	ch := make(chan stream.Event, 4)
	switch call {
	case 0:
		// First call: emit a todo add call (creating a plan).
		go func() {
			defer close(ch)
			ch <- stream.ToolCallStart{Name: "todo"}
			ch <- stream.ToolCallDelta{Delta: `{"action":"add","content":"Step 1: explore"}`}
			ch <- stream.ToolCall{ID: "tc-todo", Name: "todo", Arguments: `{"action":"add","content":"Step 1: explore"}`}
			ch <- stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}}
		}()
	case 1:
		// Second call: now that plan exists, emit a bash call.
		go func() {
			defer close(ch)
			ch <- stream.ToolCallStart{Name: "bash"}
			ch <- stream.ToolCallDelta{Delta: `{"command":"echo ok"}`}
			ch <- stream.ToolCall{ID: "tc-bash", Name: "bash", Arguments: `{"command":"echo ok"}`}
			ch <- stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}}
		}()
	default:
		// Finish.
		close(ch)
	}
	return ch, nil
}

// TestRun_PlanGuard_AllowsBashAfterTodoPlan verifies that once a todo
// item exists, the guard lets other tools through.
func TestRun_PlanGuard_AllowsBashAfterTodoPlan(t *testing.T) {
	p := &planGuardTodoProvider{}
	d := &captureDisplay{}
	msgs := []providers.RichMessage{
		{Role: "user", Content: []providers.ContentBlock{{Type: "text", Text: "do something"}}},
	}

	_, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "pgt-1",
		MaxRetries: 1,
		HasTodos:   func() bool { return true }, // simulates: plan exists
		Tools:      newMockToolRunner(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// bash tool call should NOT have the plan-required error.
	for _, end := range d.toolCallEnds {
		if end.Name == "bash" && strings.Contains(end.Result, "todo tool") {
			t.Error("bash tool call should NOT be blocked after plan was created")
		}
	}
}

// ---------------------------------------------------------------------------
// Tests for "all done" guard re-engagement
// ---------------------------------------------------------------------------

// planGuardAllDoneProvider emits a bash tool call on the first invocation,
// then finishes (no tool calls). Used to verify that the guard blocks
// when all todos are done.
type planGuardAllDoneProvider struct {
	mu     sync.Mutex
	called bool
}

func (p *planGuardAllDoneProvider) Name() string         { return "pgad" }
func (p *planGuardAllDoneProvider) IsConfigured() bool   { return true }
func (p *planGuardAllDoneProvider) Models() []string     { return []string{"pgad-1"} }
func (p *planGuardAllDoneProvider) FreeModels() []string { return nil }

func (p *planGuardAllDoneProvider) Stream(ctx context.Context, req providers.Request) (<-chan stream.Event, error) {
	p.mu.Lock()
	first := !p.called
	p.called = true
	p.mu.Unlock()

	ch := make(chan stream.Event, 4)
	if first {
		go func() {
			defer close(ch)
			ch <- stream.ToolCallStart{Name: "bash"}
			ch <- stream.ToolCallDelta{Delta: `{"command":"ls"}`}
			ch <- stream.ToolCall{ID: "tc-bash", Name: "bash", Arguments: `{"command":"ls"}`}
			ch <- stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}}
		}()
	} else {
		close(ch)
	}
	return ch, nil
}

// TestEnforcePlanGuard_AllDone_BlocksNonTodo verifies that when the
// HasTodos callback returns false because all items are "done", the
// guard blocks non-todo tools — the LLM must create a new plan.
func TestEnforcePlanGuard_AllDone_BlocksNonTodo(t *testing.T) {
	// Simulate: HasPendingTodos() would return false because all done.
	cfg := Config{HasTodos: func() bool { return false }}
	calls := []stream.ToolCall{
		{Name: "bash", ID: "1", Arguments: `{"command":"echo hello"}`},
	}
	toExec, _, results := enforcePlanGuard(cfg, calls)
	if len(toExec) != 0 {
		t.Fatalf("expected 0 calls to execute, got %d", len(toExec))
	}
	if !strings.Contains(results[0], "todo tool") {
		t.Errorf("expected plan-required error, got: %s", results[0])
	}
}

// TestEnforcePlanGuard_AllDone_AllowsTodoTool verifies that even when
// the guard is active (all items done), the LLM can still use the
// todo tool to create a new plan.
func TestEnforcePlanGuard_AllDone_AllowsTodoTool(t *testing.T) {
	cfg := Config{HasTodos: func() bool { return false }}
	calls := []stream.ToolCall{
		{Name: "todo", ID: "1", Arguments: `{"action":"add","content":"new plan"}`},
	}
	toExec, idx, results := enforcePlanGuard(cfg, calls)
	if len(toExec) != 1 {
		t.Fatalf("expected 1 todo call to execute, got %d", len(toExec))
	}
	if toExec[0].Name != "todo" {
		t.Errorf("expected todo call, got %q", toExec[0].Name)
	}
	if len(idx) != 1 || idx[0] != 0 {
		t.Errorf("expected origIdx=[0], got %v", idx)
	}
	if results[0] != "" {
		t.Errorf("expected empty result for todo call (not pre-filled), got %q", results[0])
	}
}

// TestEnforcePlanGuard_AllDone_MixedBatch verifies that in a batch
// containing both todo and non-todo calls when all items are done,
// only the todo calls pass through; others get blocked.
func TestEnforcePlanGuard_AllDone_MixedBatch(t *testing.T) {
	cfg := Config{HasTodos: func() bool { return false }}
	calls := []stream.ToolCall{
		{Name: "read", ID: "1", Arguments: `{"path":"/tmp/f"}`},
		{Name: "todo", ID: "2", Arguments: `{"action":"add","content":"replan"}`},
		{Name: "bash", ID: "3", Arguments: `{"command":"ls"}`},
		{Name: "todo", ID: "4", Arguments: `{"action":"add","content":"step 2"}`},
	}
	toExec, idx, results := enforcePlanGuard(cfg, calls)

	if len(toExec) != 2 {
		t.Fatalf("expected 2 todo calls to execute, got %d", len(toExec))
	}
	for _, tc := range toExec {
		if tc.Name != "todo" {
			t.Errorf("expected only todo calls in toExecute, got %q", tc.Name)
		}
	}
	if len(idx) != 2 || idx[0] != 1 || idx[1] != 3 {
		t.Errorf("expected origIdx=[1,3], got %v", idx)
	}
	// Non-todo results blocked, todo results empty (pending execution).
	if !strings.Contains(results[0], "todo tool") {
		t.Errorf("results[0] (read) should be blocked: %q", results[0])
	}
	if results[1] != "" {
		t.Errorf("results[1] (todo) should be empty: %q", results[1])
	}
	if !strings.Contains(results[2], "todo tool") {
		t.Errorf("results[2] (bash) should be blocked: %q", results[2])
	}
	if results[3] != "" {
		t.Errorf("results[3] (todo) should be empty: %q", results[3])
	}
}

// TestRun_PlanGuard_AllDone_BlocksBash is a full integration test:
// LLM has completed all todos → tries bash → gets blocked → must use todo.
func TestRun_PlanGuard_AllDone_BlocksBash(t *testing.T) {
	p := &planGuardAllDoneProvider{}
	d := &captureDisplay{}
	msgs := []providers.RichMessage{
		{Role: "user", Content: []providers.ContentBlock{{Type: "text", Text: "do something else"}}},
	}

	_, err := Run(context.Background(), p, d, &msgs, Config{
		Model:      "pgad-1",
		MaxRetries: 1,
		HasTodos:   func() bool { return false }, // all items are "done"
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	found := false
	for _, end := range d.toolCallEnds {
		if end.Name == "bash" && strings.Contains(end.Result, "todo tool") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected bash to be blocked when all todos are done")
		for _, end := range d.toolCallEnds {
			t.Logf("  toolCallEnd: name=%q result=%q", end.Name, end.Result)
		}
	}
}
