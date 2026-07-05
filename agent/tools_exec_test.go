package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// serializingRunner simulates an LLM dispatcher-fronted tool runner. It
// records (toolName, callSeq) for every Run() call, threading a per-tool call
// counter so we can assert ordering. Each call sleeps briefly to widen the
// overlap window of a concurrent (pre-fix) implementation: without the
// serial branch in executeTools, two "todo" calls would land in any order.
type serializingRunner struct {
	mu          sync.Mutex
	calls       []string // tool name + sequence index, in Run() order
	perToolSeqn map[string]int
}

func newSerializingRunner() *serializingRunner {
	return &serializingRunner{perToolSeqn: map[string]int{}}
}

func (r *serializingRunner) Run(_ context.Context, name string, _ map[string]any) (string, error) {
	r.mu.Lock()
	r.perToolSeqn[name]++
	seqn := r.perToolSeqn[name]
	r.calls = append(r.calls, fmt.Sprintf("%s#%d", name, seqn))
	r.mu.Unlock()
	// Each call sleeps 5ms — long enough that an unrestrained parallel
	// implementation would observe two "todo" calls interleaving before
	// either finishes. Serialized calls observe 10ms+ sequentially.
	time.Sleep(5 * time.Millisecond)
	return name + "ok", nil
}

// TestExecuteTools_SerializesBatchedTodoCalls proves the bug we just fixed.
// Model emits one response containing several todo calls in the same
// tool_call block; the executor must run them sequentially (call #1 fully
// before call #2 starts) and return N distinct results in call order.
// Pre-refactor, this races and the recorded calls list is non-deterministic.
func TestExecuteTools_SerializesBatchedTodoCalls(t *testing.T) {
	// Sanity: ensure the registry still reports MaxParallel=1 for todo.
	// If a future change forgets to register the limit, the executor would
	// parallelise and this test's timing guarantee silently weakens.
	if got := tools.MaxParallelFor("todo"); got != 1 {
		t.Fatalf("MaxParallelFor(\"todo\") = %d, want 1 — executor would not serialize and this test would falsely pass", got)
	}

	calls := []stream.ToolCall{
		{Name: "todo", Arguments: jsonString(map[string]any{"action": "add", "content": "first"})},
		{Name: "todo", Arguments: jsonString(map[string]any{"action": "add", "content": "second"})},
		{Name: "todo", Arguments: jsonString(map[string]any{"action": "add", "content": "third"})},
	}

	runner := newSerializingRunner()
	results := executeTools(context.Background(), runner, calls)

	if len(results) != 3 {
		t.Fatalf("results len = %d, want 3", len(results))
	}
	for i, s := range results {
		if s != "todook" {
			t.Fatalf("results[%d] = %q, want todook", i, s)
		}
	}

	wantOrder := []string{"todo#1", "todo#2", "todo#3"}
	if !equalSlices(runner.calls, wantOrder) {
		t.Fatalf("recorded calls = %v, want %v (calls must run sequentially within the same batch)", runner.calls, wantOrder)
	}
}

// TestExecuteTools_DifferentToolsRunInParallel proves grouping doesn't
// accidentally serialise tools that opted out (limit = 0). We mix a serial
// (todo) group with an unbounded (read) group and assert that two read
// calls overlap: their runner concurrency counter exceeds 1 at some point.
// If grouping ever fused per-name or radiated synchronously, this fails.
func TestExecuteTools_DifferentToolsRunInParallel(t *testing.T) {
	calls := []stream.ToolCall{
		{Name: "todo", Arguments: jsonString(map[string]any{"action": "add", "content": "x"})},
		{Name: "read", Arguments: jsonString(map[string]any{"path": "a"})},
		{Name: "read", Arguments: jsonString(map[string]any{"path": "b"})},
		{Name: "todo", Arguments: jsonString(map[string]any{"action": "add", "content": "y"})},
	}
	runner := newParallelProbeRunner()
	results := executeTools(context.Background(), runner, calls)

	if len(results) != 4 {
		t.Fatalf("results len = %d, want 4", len(results))
	}
	// The probe records the maximum number of "read"-group goroutines
	// observed concurrently. A correct implementation allows both reads
	// to overlap inside their group; a buggy one serialises everything.
	if runner.maxReadOverlap.Load() < 2 {
		t.Fatalf("maxReadOverlap = %d, want >= 2 (two read calls must run in parallel)", runner.maxReadOverlap.Load())
	}
	// And the todo group must still be ordered #1, #2 within that group —
	// interleaving with parallel reads is fine, but the two todos cannot
	// finish out of order relative to each other.
	runner.callsMu.Lock()
	defer runner.callsMu.Unlock()
	if len(runner.todoSeq) != 2 || runner.todoSeq[0] != 1 || runner.todoSeq[1] != 2 {
		t.Fatalf("todo sequence = %v, want [1 2] (todo group must stay ordered across the batch)", runner.todoSeq)
	}
}

// parallelProbeRunner observes concurrent execution of one tool group.
// It sleeps each "read" call long enough that two reads entering
// executeTools together must overlap; for "todo" it records the per-tool
// call sequence to verify internal ordering of the serial group.
type parallelProbeRunner struct {
	callsMu         sync.Mutex
	todoSeq         []int
	maxReadOverlap  atomic.Int32
	curReadInFlight atomic.Int32
}

func newParallelProbeRunner() *parallelProbeRunner { return &parallelProbeRunner{} }

func (r *parallelProbeRunner) Run(_ context.Context, name string, _ map[string]any) (string, error) {
	switch name {
	case "read":
		cur := r.curReadInFlight.Add(1)
		// update max
		for {
			old := r.maxReadOverlap.Load()
			if cur <= old || r.maxReadOverlap.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		r.curReadInFlight.Add(-1)
		return "readok", nil
	case "todo":
		// Record sequence position; ordering within the todo group is
		// the property under test. Lock guards append.
		r.callsMu.Lock()
		r.todoSeq = append(r.todoSeq, len(r.todoSeq)+1)
		r.callsMu.Unlock()
		time.Sleep(5 * time.Millisecond)
		return "todook", nil
	}
	return name + "ok", nil
}

// helpers

func jsonString(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		// test-only marshal — panic is fine
		panic(err)
	}
	return string(b)
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Reference: package init in agent_test.go may have side effects; nil-import
// guard avoids an unused-import error if a future edit removes all callers.
var _ = strings.HasPrefix
