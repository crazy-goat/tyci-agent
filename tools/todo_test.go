package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
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
	for _, sub := range []string{"1. [todo] alpha", "2. [todo] beta", "3. [doing] gamma"} {
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

// TestTodoAcceptsTheStatusNamesModelsActuallyUse. Models arrive with
// "in_progress", "pending" and "completed" trained into them by other
// harnesses. Rejecting those bought nothing — the intent is unambiguous every
// time — and cost a wasted call; one observed session answered the refusal by
// retrying the identical call.
func TestTodoAcceptsTheStatusNamesModelsActuallyUse(t *testing.T) {
	cases := map[string]string{
		"in_progress": "doing",
		"in-progress": "doing",
		"active":      "doing",
		"pending":     "todo",
		"not_started": "todo",
		"completed":   "done",
		"finished":    "done",
		"cancelled":   "blocked",
		"IN_PROGRESS": "doing",
		"  doing  ":   "doing",
	}
	for in, want := range cases {
		if got := canonicalStatus(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// TestTodoStillRejectsNonsense: a status nobody can guess the meaning of is a
// real error, and mapping it to something would be a guess with consequences.
func TestTodoStillRejectsNonsense(t *testing.T) {
	for _, in := range []string{"banana", "almost", "77", "maybe-done"} {
		if validStatus(canonicalStatus(in)) {
			t.Errorf("%q was accepted", in)
		}
	}
}

// TestTodoAddBatchAcceptsInProgress is the exact call from a real session: the
// model set status="in_progress" on a batch item and the whole batch failed.
func TestTodoAddBatchAcceptsInProgress(t *testing.T) {
	ClearTodoList()
	t.Cleanup(ClearTodoList)

	res := RunTool(context.Background(), "todo", map[string]any{
		"action": "add_batch",
		"items": []any{
			map[string]any{"content": "Read CONTRIBUTING.md", "status": "in_progress"},
			map[string]any{"content": "Pick an issue"},
		},
	})
	if !res.Success {
		t.Fatalf("the batch was refused: %s", res.Error)
	}
	if !strings.Contains(res.Content, "doing") {
		t.Errorf("the first item should be \"doing\": %q", res.Content)
	}
}

// TestTodoUpdateAcceptsInProgress covers the other path a status arrives by.
func TestTodoUpdateAcceptsInProgress(t *testing.T) {
	ClearTodoList()
	t.Cleanup(ClearTodoList)

	if res := RunTool(context.Background(), "todo", map[string]any{
		"action": "add", "content": "do the thing",
	}); !res.Success {
		t.Fatalf("setup: %s", res.Error)
	}
	res := RunTool(context.Background(), "todo", map[string]any{
		"action": "update", "id": 1, "status": "in_progress",
	})
	if !res.Success {
		t.Fatalf("the update was refused: %s", res.Error)
	}
	if !strings.Contains(res.Content, "doing") {
		t.Errorf("got %q", res.Content)
	}
}

// ─── per-agent isolation (item 24) ───────────────────────────────────────
//
// These tests use context.Background() for the "main" conversation (no
// TodoAgentCtxKey/JobIDCtxKey set) and a context carrying TodoAgentCtxKey
// (for a subagent) or JobIDCtxKey (for a /btw side-conversation) to
// simulate a child. subagent.go's runSingleTask sets TodoAgentCtxKey for
// every real subagent call; btw.go's startBtw sets JobIDCtxKey for every
// /btw job — this exercises the exact resolution todoAgentIDFromCtx does
// for both.

func childTodoCtx(agentID string) context.Context {
	return context.WithValue(context.Background(), TodoAgentCtxKey{}, agentID)
}

// resetTodoStoreForTest wipes EVERY agent's list — main and all children —
// unlike ClearTodoList (correctly, in production) which only ever touches
// main. Any test that creates a child list via childTodoCtx must reset with
// this, not ClearTodoList, in t.Cleanup: otherwise the child list leaks into
// whichever test runs next, and eviction-sensitive assertions (how many
// child lists exist, which one is oldest) become dependent on test order and
// -count, exactly how the original eviction-on-creation bug (see
// TestEviction_DropsOldestTerminalChildList_KeepsNewestAndMain) hid behind
// `go test -count=1`.
func resetTodoStoreForTest() {
	todoStore.Lock()
	todoStore.agents = map[string]*todoAgentList{}
	todoStore.Unlock()
}

// TestChildTodos_DoNotAppearInParentList reverts to: a child's add lands in
// AllTodoItems() (the main/TUI-facing list) too.
func TestChildTodos_DoNotAppearInParentList(t *testing.T) {
	resetTodoStoreForTest()
	t.Cleanup(resetTodoStoreForTest)

	tool := &TodoTool{}
	if res := tool.Run(context.Background(), map[string]any{"action": "add", "content": "main plan"}); !res.Success {
		t.Fatalf("main add failed: %s", res.Error)
	}
	if res := tool.Run(childTodoCtx("child-1"), map[string]any{"action": "add", "content": "child scratch work"}); !res.Success {
		t.Fatalf("child add failed: %s", res.Error)
	}

	items := AllTodoItems()
	if len(items) != 1 || items[0].Content != "main plan" {
		t.Fatalf("main list contaminated by child: %+v", items)
	}
}

// TestChildClear_DoesNotWipeParentList reverts to: todo(action="clear") in
// a child wipes the parent's plan (the exact /new-defeating bug in the
// TODO.md item).
func TestChildClear_DoesNotWipeParentList(t *testing.T) {
	resetTodoStoreForTest()
	t.Cleanup(resetTodoStoreForTest)

	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "main plan"})
	tool.Run(childTodoCtx("child-2"), map[string]any{"action": "add", "content": "child scratch"})

	if res := tool.Run(childTodoCtx("child-2"), map[string]any{"action": "clear"}); !res.Success {
		t.Fatalf("child clear failed: %s", res.Error)
	}

	items := AllTodoItems()
	if len(items) != 1 || items[0].Content != "main plan" {
		t.Fatalf("child's clear wiped (or corrupted) the parent's list: %+v", items)
	}
}

// TestIds_DoNotCollideAcrossAgents reverts to: every agent shares one id
// sequence, so a parent's update by id can land on a child's item (or vice
// versa) once both lists exist in the same numbering space.
func TestIds_DoNotCollideAcrossAgents(t *testing.T) {
	resetTodoStoreForTest()
	t.Cleanup(resetTodoStoreForTest)

	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "parent item"})
	tool.Run(childTodoCtx("child-3"), map[string]any{"action": "add", "content": "child item"})

	// Both lists start their own sequence at 1 — this is only possible if
	// they are genuinely separate id spaces, not one shared counter.
	parentList := tool.Run(context.Background(), map[string]any{"action": "list"}).Content
	childList := tool.Run(childTodoCtx("child-3"), map[string]any{"action": "list"}).Content
	if !strings.Contains(parentList, "1. [todo] parent item") {
		t.Fatalf("parent item should be id 1, got: %q", parentList)
	}
	if !strings.Contains(childList, "1. [todo] child item") {
		t.Fatalf("child item should be id 1 in its OWN list, got: %q", childList)
	}

	// The parent updating "its" id=1 must never touch the child's id=1.
	if res := tool.Run(context.Background(), map[string]any{"action": "update", "id": 1, "content": "parent item RENAMED"}); !res.Success {
		t.Fatalf("parent update failed: %s", res.Error)
	}

	parentList = tool.Run(context.Background(), map[string]any{"action": "list"}).Content
	childList = tool.Run(childTodoCtx("child-3"), map[string]any{"action": "list"}).Content
	if !strings.Contains(parentList, "parent item RENAMED") || strings.Contains(parentList, "child item") {
		t.Fatalf("parent list wrong after its own update: %q", parentList)
	}
	if !strings.Contains(childList, "child item") || strings.Contains(childList, "RENAMED") {
		t.Fatalf("child's item was hit by the parent's update id=1: %q", childList)
	}
}

// TestChild_DoesNotInheritParentPendingTodosForPlanGuard reverts to: a
// freshly-spawned child sees the parent's already-open todos as its own,
// so a plan guard wired against a child's own list (todoAgentIDFromCtx)
// would wrongly conclude the child already has a plan.
func TestChild_DoesNotInheritParentPendingTodosForPlanGuard(t *testing.T) {
	resetTodoStoreForTest()
	t.Cleanup(resetTodoStoreForTest)

	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "main open work"})
	if !HasPendingTodos() {
		t.Fatal("setup: main should have pending work")
	}

	res := tool.Run(childTodoCtx("child-4"), map[string]any{"action": "list"})
	if !strings.Contains(res.Content, "Todo list is empty") {
		t.Fatalf("a brand-new child should start with no plan, inherited or otherwise; got: %q", res.Content)
	}
}

// TestTodoCounts_FollowMainWhileChildWrites reverts to: TodoCounts() (the
// TUI top bar) reads whichever list was written last, instead of always
// the main conversation's.
func TestTodoCounts_FollowMainWhileChildWrites(t *testing.T) {
	resetTodoStoreForTest()
	t.Cleanup(resetTodoStoreForTest)

	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "main a"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "main b"})
	tool.Run(context.Background(), map[string]any{"action": "done", "id": 1})

	if done, total := TodoCounts(); done != 1 || total != 2 {
		t.Fatalf("before child writes: got done=%d total=%d, want 1/2", done, total)
	}

	// A child furiously writing its own (larger, differently-shaped) list
	// must not move the main counts at all.
	for i := 0; i < 5; i++ {
		tool.Run(childTodoCtx("child-5"), map[string]any{"action": "add", "content": fmt.Sprintf("child %d", i)})
	}
	tool.Run(childTodoCtx("child-5"), map[string]any{"action": "done", "id": 1})
	tool.Run(childTodoCtx("child-5"), map[string]any{"action": "done", "id": 2})

	if done, total := TodoCounts(); done != 1 || total != 2 {
		t.Fatalf("after child writes: got done=%d total=%d, want 1/2 (main counts must not follow the child)", done, total)
	}
}

// TestBtw_GetsOwnTodoListViaJobIDCtxKey reverts to: /btw side-conversations
// share the main list. btw.go's startBtw sets JobIDCtxKey (not
// TodoAgentCtxKey) on the job context it runs on, so todoAgentIDFromCtx
// must fall back to it — this is the mechanism that gives /btw its own
// list without btw.go needing to know anything about todo state.
func TestBtw_GetsOwnTodoListViaJobIDCtxKey(t *testing.T) {
	resetTodoStoreForTest()
	t.Cleanup(resetTodoStoreForTest)

	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "main plan"})

	btwCtx := context.WithValue(context.Background(), JobIDCtxKey{}, "btw-job-1")
	tool.Run(btwCtx, map[string]any{"action": "add", "content": "btw scratch"})
	if res := tool.Run(btwCtx, map[string]any{"action": "clear"}); !res.Success {
		t.Fatalf("btw clear failed: %s", res.Error)
	}

	items := AllTodoItems()
	if len(items) != 1 || items[0].Content != "main plan" {
		t.Fatalf("/btw contaminated (or its clear wiped) the main list: %+v", items)
	}
}

// TestTodoAgentCtxKey_TakesPriorityOverJobIDCtxKey pins the resolution
// order documented on todoAgentIDFromCtx: an async subagent job's context
// carries both keys (JobIDCtxKey from runAsync, TodoAgentCtxKey from the
// runSingleTask call inside it), and the subagent's own id must win so it
// gets a list distinct from the job's.
func TestTodoAgentCtxKey_TakesPriorityOverJobIDCtxKey(t *testing.T) {
	resetTodoStoreForTest()
	t.Cleanup(resetTodoStoreForTest)

	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, "job-1")
	ctx = context.WithValue(ctx, TodoAgentCtxKey{}, "subagent-1")

	if got := todoAgentIDFromCtx(ctx); got != "subagent-1" {
		t.Fatalf("todoAgentIDFromCtx = %q, want %q", got, "subagent-1")
	}
}

// TestChildPendingTodos_DoNotLeakIntoMainPlanGuard is the child→parent
// direction TestChild_DoesNotInheritParentPendingTodosForPlanGuard does not
// cover: it only checked that a fresh child starts with no plan of its
// own. This checks the actual leak the TODO.md item was filed for — a
// child's own todo(add) making the MAIN plan guard (HasPendingTodos, wired
// at commands.go:173) believe the main conversation has an open plan when
// it never touched the todo tool itself.
func TestChildPendingTodos_DoNotLeakIntoMainPlanGuard(t *testing.T) {
	resetTodoStoreForTest()
	t.Cleanup(resetTodoStoreForTest)

	if HasPendingTodos() {
		t.Fatal("setup: main should start with no pending todos")
	}

	tool := &TodoTool{}
	tool.Run(childTodoCtx("child-7"), map[string]any{"action": "add", "content": "child work"})

	if HasPendingTodos() {
		t.Error("a child's pending todo leaked into the main conversation's plan guard")
	}
}

// TestEviction_DropsOldestTerminalChildList_KeepsNewestAndMain reverts to
// the eviction blocker: getOrCreateLocked used to insert the new entry
// before running eviction, with lastActivity still at its zero time, so
// evictOldChildListsLocked's ascending sort always picked the entry that
// had JUST been created — evicted the instant it existed, once 50 (child)
// lists were already present. The caller then kept writing through a
// pointer to state nothing else could reach: todo(add) reported
// success=true, and the very next todo(list) on the same agent said "Todo
// list is empty."
//
// maxRetainedChildTodoLists+1 terminal children are created (oldest
// first, strictly increasing lastActivity), then one more, untouched
// child is created purely to trigger the next eviction sweep —
// evictOldChildListsLocked only runs opportunistically inside
// getOrCreateLocked when a NEW agent id is created (mirroring
// jobs/registry.go's pruneTerminalLocked, which is likewise only run from
// Start), so the 51st terminal list's own creation is too early to see
// itself as the one pushing the pool over the bound.
func TestEviction_DropsOldestTerminalChildList_KeepsNewestAndMain(t *testing.T) {
	resetTodoStoreForTest()
	t.Cleanup(resetTodoStoreForTest)

	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "main plan"})

	ids := make([]string, 0, maxRetainedChildTodoLists+1)
	for i := 0; i < maxRetainedChildTodoLists+1; i++ {
		id := fmt.Sprintf("evict-child-%d", i)
		ids = append(ids, id)
		if res := tool.Run(childTodoCtx(id), map[string]any{"action": "add", "content": "plan " + id}); !res.Success {
			t.Fatalf("setup: add for %s failed: %s", id, res.Error)
		}
		MarkTodoAgentDone(id)
		time.Sleep(time.Millisecond) // keep lastActivity strictly increasing
	}
	oldest, newest := ids[0], ids[len(ids)-1]

	// Trigger the eviction sweep: creating this new, unrelated agent is
	// the event that runs evictOldChildListsLocked with all 51 terminal
	// lists above already in the map.
	tool.Run(childTodoCtx("evict-trigger"), map[string]any{"action": "add", "content": "trigger"})

	if res := tool.Run(childTodoCtx(oldest), map[string]any{"action": "list"}); !strings.Contains(res.Content, "Todo list is empty") {
		t.Errorf("oldest terminal child (%s) should have been evicted, got: %q", oldest, res.Content)
	}
	if res := tool.Run(childTodoCtx(newest), map[string]any{"action": "list"}); !strings.Contains(res.Content, "plan "+newest) {
		t.Errorf("newest child (%s) should survive eviction, got: %q", newest, res.Content)
	}
	if items := AllTodoItems(); len(items) != 1 || items[0].Content != "main plan" {
		t.Errorf("main's list should be untouched by child eviction, got: %+v", items)
	}
}

// ─── CopyTodoListForResume (item 43) ─────────────────────────────────────
//
// `resume` re-keys a stored conversation onto a brand-new job id
// (btw.go's jobResumerAdapter.Resume). Without a copy, the resumed job's
// todo list starts empty even though the forked transcript's own past
// todo(...) calls/results still name ids from the OLD job's list — a
// resumed agent reading its own history sees ids that now silently
// resolve to nothing.

func TestCopyTodoListForResume_CarriesItemsAndNextID(t *testing.T) {
	t.Cleanup(resetTodoStoreForTest)
	tool := &TodoTool{}

	tool.Run(childTodoCtx("old-job"), map[string]any{"action": "add", "content": "first"})
	tool.Run(childTodoCtx("old-job"), map[string]any{"action": "add", "content": "second"})
	tool.Run(childTodoCtx("old-job"), map[string]any{"action": "doing", "id": 1})

	CopyTodoListForResume("old-job", "new-job")

	res := tool.Run(childTodoCtx("new-job"), map[string]any{"action": "list"})
	if !res.Success {
		t.Fatalf("list on resumed job failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "first") || !strings.Contains(res.Content, "second") {
		t.Errorf("resumed job's list should carry the old job's items, got: %q", res.Content)
	}
	if !strings.Contains(res.Content, "[doing]") {
		t.Errorf("resumed job's list should carry the old job's statuses, got: %q", res.Content)
	}

	// The old job's own list must be untouched — a copy, not a move — since
	// the old (terminal) job's list may still be inspected (e.g. item 1's
	// Subagents tab) independently of the resume.
	oldRes := tool.Run(childTodoCtx("old-job"), map[string]any{"action": "list"})
	if !strings.Contains(oldRes.Content, "first") || !strings.Contains(oldRes.Content, "second") {
		t.Errorf("old job's own list should be unchanged after copy, got: %q", oldRes.Content)
	}

	// nextID carries forward too, so a NEW item added post-resume does not
	// collide with an id already referenced by the forked transcript's
	// history (e.g. an old assistant turn discussing "item 2").
	res = tool.Run(childTodoCtx("new-job"), map[string]any{"action": "add", "content": "third"})
	if !res.Success || !strings.Contains(res.Content, "3. [todo] third") {
		t.Errorf("expected the new item to continue the old id sequence at 3, got: %v %q", res.Success, res.Content)
	}
}

func TestCopyTodoListForResume_NoOldListIsANoOp(t *testing.T) {
	t.Cleanup(resetTodoStoreForTest)
	tool := &TodoTool{}

	CopyTodoListForResume("never-existed", "new-job")

	res := tool.Run(childTodoCtx("new-job"), map[string]any{"action": "list"})
	if !strings.Contains(res.Content, "Todo list is empty") {
		t.Errorf("expected an empty list when the old job never had one, got: %q", res.Content)
	}
}

func TestCopyTodoListForResume_ChainedResumeCarriesForward(t *testing.T) {
	t.Cleanup(resetTodoStoreForTest)
	tool := &TodoTool{}

	tool.Run(childTodoCtx("job-1"), map[string]any{"action": "add", "content": "only item"})
	CopyTodoListForResume("job-1", "job-2")
	tool.Run(childTodoCtx("job-2"), map[string]any{"action": "add", "content": "added after first resume"})
	CopyTodoListForResume("job-2", "job-3")

	res := tool.Run(childTodoCtx("job-3"), map[string]any{"action": "list"})
	if !strings.Contains(res.Content, "only item") || !strings.Contains(res.Content, "added after first resume") {
		t.Errorf("a chained resume should carry the whole accumulated list forward, got: %q", res.Content)
	}
}

// TestCopyTodoListForResume_RefusesMainAgentIDAsSource pins the zero-value
// footgun a reviewer flagged: mainAgentTodoID is "", the same value an
// oldAgentID left unset (a resumableEntry.todoAgentID that was never
// assigned) would have — this must refuse, not silently copy the whole
// main conversation's list into a resumed background job.
func TestCopyTodoListForResume_RefusesMainAgentIDAsSource(t *testing.T) {
	t.Cleanup(resetTodoStoreForTest)
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "main's own plan"})

	CopyTodoListForResume(mainAgentTodoID, "new-job")

	res := tool.Run(childTodoCtx("new-job"), map[string]any{"action": "list"})
	if !strings.Contains(res.Content, "Todo list is empty") {
		t.Errorf("expected an empty list, main's plan must never leak via an unset/zero-value source id, got: %q", res.Content)
	}
}

func TestCopyTodoListForResume_RefusesMainAgentID(t *testing.T) {
	t.Cleanup(resetTodoStoreForTest)
	tool := &TodoTool{}
	tool.Run(childTodoCtx("old-job"), map[string]any{"action": "add", "content": "should not leak to main"})

	CopyTodoListForResume("old-job", mainAgentTodoID)

	if items := AllTodoItems(); len(items) != 0 {
		t.Errorf("main list must never be overwritten by CopyTodoListForResume, got: %+v", items)
	}
}
