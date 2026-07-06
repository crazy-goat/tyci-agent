package tools

import (
	"context"
	"strings"
	"testing"
)

func TestTodoTool_StatusAliases(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	res := tool.Run(context.Background(), map[string]any{"action": "add", "content": "x"})
	if !res.Success {
		t.Fatalf("add failed: %s", res.Error)
	}
	res = tool.Run(context.Background(), map[string]any{"action": "doing", "id": 1})
	if !res.Success || !strings.Contains(res.Content, "[doing]") {
		t.Fatalf("doing failed: %v %s %s", res.Success, res.Content, res.Error)
	}
}

func TestTodoTool_ClearAndList(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	res := tool.Run(context.Background(), map[string]any{"action": "add", "content": "first"})
	if !res.Success {
		t.Fatalf("add failed: %s", res.Error)
	}
	res = tool.Run(context.Background(), map[string]any{"action": "add", "content": "second"})
	if !res.Success {
		t.Fatalf("add failed: %s", res.Error)
	}
	res = tool.Run(context.Background(), map[string]any{"action": "list"})
	if !res.Success {
		t.Fatalf("list failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "first") || !strings.Contains(res.Content, "second") {
		t.Fatalf("expected both items, got: %s", res.Content)
	}
	res = tool.Run(context.Background(), map[string]any{"action": "clear"})
	if !res.Success {
		t.Fatalf("clear failed: %s", res.Error)
	}
	res = tool.Run(context.Background(), map[string]any{"action": "list"})
	if !strings.Contains(res.Content, "Todo list is empty") {
		t.Fatalf("expected empty list, got: %s", res.Content)
	}
}

// add_batch is the parallelism-friendly alternative: one call appends many
// items and returns the full list in a single round-trip. These tests pin
// down happy-path assignment, atomicity on bad inputs, and the surface
// errors the LLM will see.
func TestTodoTool_AddBatch_HappyPath(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})

	res := tool.Run(context.Background(), map[string]any{
		"action": "add_batch",
		"items": []any{
			map[string]any{"content": "alpha"},
			map[string]any{"content": "beta", "priority": "high"},
			map[string]any{"content": "gamma", "status": "doing", "priority": "low"},
		},
	})
	if !res.Success {
		t.Fatalf("add_batch failed: %s", res.Error)
	}
	// All three ids rendered in the formatted full list.
	for _, sub := range []string{"1. [todo] normal alpha", "2. [todo] high beta", "3. [doing] low gamma"} {
		if !strings.Contains(res.Content, sub) {
			t.Fatalf("expected rendered line %q in result, got:\n%s", sub, res.Content)
		}
	}

	// A list call returns the same shape, no drift.
	res = tool.Run(context.Background(), map[string]any{"action": "list"})
	if !res.Success {
		t.Fatalf("list failed: %s", res.Error)
	}
	for _, sub := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(res.Content, sub) {
			t.Fatalf("list missing %q, got:\n%s", sub, res.Content)
		}
	}
}

func TestTodoTool_AddBatch_AssignsConsecutiveIds(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "pre-existing"})

	res := tool.Run(context.Background(), map[string]any{
		"action": "add_batch",
		"items": []any{
			map[string]any{"content": "x"},
			map[string]any{"content": "y"},
		},
	})
	if !res.Success {
		t.Fatalf("add_batch failed: %s", res.Error)
	}
	// First add took id 1, batch should take 2 and 3 — not 1 and 2 again.
	for _, sub := range []string{"2. [", "3. ["} {
		if !strings.Contains(res.Content, sub) {
			t.Fatalf("expected line with %q, got:\n%s", sub, res.Content)
		}
	}
}

func TestTodoTool_AddBatch_WithParentID(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "parent"})

	res := tool.Run(context.Background(), map[string]any{
		"action": "add_batch",
		"items": []any{
			map[string]any{"content": "child-a", "parentId": 1},
			map[string]any{"content": "child-b", "parentId": 1},
		},
	})
	if !res.Success {
		t.Fatalf("add_batch failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "parent:1 child-a") {
		t.Fatalf("expected parent:1 child-a line, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "parent:1 child-b") {
		t.Fatalf("expected parent:1 child-b line, got:\n%s", res.Content)
	}
}

func TestTodoTool_AddBatch_AtomicOnBadStatusInMiddle(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})

	res := tool.Run(context.Background(), map[string]any{
		"action": "add_batch",
		"items": []any{
			map[string]any{"content": "good-before"},
			map[string]any{"content": "bad-one", "status": "nope"},
			map[string]any{"content": "good-after"},
		},
	})
	if res.Success {
		t.Fatalf("expected failure on bad status, got: %s", res.Content)
	}
	if !strings.Contains(res.Error, "items[1]") || !strings.Contains(res.Error, "invalid status") {
		t.Fatalf("error should pinpoint items[1] and mention invalid status, got: %s", res.Error)
	}

	// And nothing got added — partial-apply would be a footgun.
	res = tool.Run(context.Background(), map[string]any{"action": "list"})
	if !strings.Contains(res.Content, "Todo list is empty") {
		t.Fatalf("expected empty list after rolled-back batch, got: %s", res.Content)
	}
}

func TestTodoTool_AddBatch_RejectsEmptyAndMissingItems(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})

	for name, args := range map[string]map[string]any{
		"missing items key":    {"action": "add_batch"},
		"items null":           {"action": "add_batch", "items": nil},
		"items wrong type":     {"action": "add_batch", "items": "not an array"},
		"items empty array":    {"action": "add_batch", "items": []any{}},
		"item missing content": {"action": "add_batch", "items": []any{map[string]any{}}},
		"item not an object":   {"action": "add_batch", "items": []any{"plain string"}},
		"bad parentId":         {"action": "add_batch", "items": []any{map[string]any{"content": "x", "parentId": 999}}},
	} {
		t.Run(name, func(t *testing.T) {
			res := tool.Run(context.Background(), args)
			if res.Success {
				t.Fatalf("expected failure, got success: %s", res.Content)
			}
		})
	}

	// None of the above should have mutated state.
	res := tool.Run(context.Background(), map[string]any{"action": "list"})
	if !strings.Contains(res.Content, "Todo list is empty") {
		t.Fatalf("expected empty list after rejected batches, got: %s", res.Content)
	}
}

// TestMaxParallelFor drives the dispatcher-side knob: TodoTool must
// advertise MaxParallel=1 so a user who batches several todo calls in
// one LLM response gets them serialised by the executor (concurrent
// calls race in-process even though each Run holds its own mutex).
// Locking down the registry value here means a future refactor that
// drops the method (or accidentally sets it to 0) fails this test.
func TestMaxParallelFor(t *testing.T) {
	if got := MaxParallelFor("todo"); got != 1 {
		t.Fatalf("MaxParallelFor(\"todo\") = %d, want 1 — the dispatcher relies on this to serialize batched todo calls", got)
	}
	if got := MaxParallelFor("read"); got != 0 {
		t.Fatalf("MaxParallelFor(\"read\") = %d, want 0 (default = unbounded)", got)
	}
	if got := MaxParallelFor("does-not-exist"); got != 0 {
		t.Fatalf("MaxParallelFor(<missing>) = %d, want 0", got)
	}
}

// HasPendingTodos tests verify that the plan-guard only considers items
// with status "todo" or "doing" as active work. Once all items are
// "done" or "blocked", the guard re-engages and blocks non-todo tools.

func TestHasPendingTodos_EmptyList(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})

	if HasPendingTodos() {
		t.Error("empty list: expected false")
	}
}

func TestHasPendingTodos_AllTodo(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "step 1"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "step 2"})

	if !HasPendingTodos() {
		t.Error("all items todo: expected true")
	}
}

func TestHasPendingTodos_AllDoing(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "wip"})
	tool.Run(context.Background(), map[string]any{"action": "doing", "id": 1})

	if !HasPendingTodos() {
		t.Error("single doing item: expected true")
	}
}

func TestHasPendingTodos_AllDone(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "step 1"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "step 2"})
	tool.Run(context.Background(), map[string]any{"action": "done", "id": 1})
	tool.Run(context.Background(), map[string]any{"action": "done", "id": 2})

	if HasPendingTodos() {
		t.Error("all items done: expected false")
	}
}

func TestHasPendingTodos_AllBlocked(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "blocked step"})
	tool.Run(context.Background(), map[string]any{"action": "blocked", "id": 1})

	if HasPendingTodos() {
		t.Error("all items blocked: expected false")
	}
}

func TestHasPendingTodos_MixedDoneAndTodo(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "done step"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "pending step"})
	tool.Run(context.Background(), map[string]any{"action": "done", "id": 1})

	if !HasPendingTodos() {
		t.Error("one done + one todo: expected true")
	}
}

func TestHasPendingTodos_MixedDoneAndDoing(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "done step"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "wip step"})
	tool.Run(context.Background(), map[string]any{"action": "done", "id": 1})
	tool.Run(context.Background(), map[string]any{"action": "doing", "id": 2})

	if !HasPendingTodos() {
		t.Error("one done + one doing: expected true")
	}
}

func TestHasPendingTodos_MixedDoneAndBlocked(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "done step"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "blocked step"})
	tool.Run(context.Background(), map[string]any{"action": "done", "id": 1})
	tool.Run(context.Background(), map[string]any{"action": "blocked", "id": 2})

	if HasPendingTodos() {
		t.Error("one done + one blocked: expected false")
	}
}

func TestHasPendingTodos_LastItemDone(t *testing.T) {
	// Simulates the exact scenario the user described: LLM had a plan,
	// completed everything, guard should re-engage.
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "investigate bug"})
	tool.Run(context.Background(), map[string]any{"action": "doing", "id": 1})
	if !HasPendingTodos() {
		t.Fatal("doing: expected true")
	}
	tool.Run(context.Background(), map[string]any{"action": "done", "id": 1})
	if HasPendingTodos() {
		t.Error("last item done: expected false — guard should re-engage")
	}
}
