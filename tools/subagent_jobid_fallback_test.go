package tools

// Item 17: every child gets a job id, in every mode — including the
// fallback path SubagentTool.Run takes when jobStarter is wired but the
// mode cannot hand a blocking call's children to the background (no
// SetBackgroundBashEnabled — `tyci run` / `--print`; see main.go/
// commands.go). Before the fix, that branch fell straight to plain
// runTasks with no job id at all, so report_progress/ask_parent/wait all
// refused to work on these children.
//
// ask_parent is the one exception, deliberately: giving these children a
// job id must not make ask_parent BLOCK for its full SubagentTimeoutSec
// with no way for an answer to ever arrive (see AskUnroutableCtxKey in
// ask.go) — a `tyci run` invocation never drains the main notice queue and the
// blocking "subagent" tool call here never returns until every child
// finishes, so nobody is ever free to call "answer_job".

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// TestNoJobRegistryStillHasNoJobID is the other side of the fix: with no
// job registry at all (jobStarter == nil — only reachable in tests; main.go
// always wires one), there is genuinely no id to hand out, so ask/
// report_progress must still refuse with "no job id", exactly as before.
func TestNoJobRegistryStillHasNoJobID(t *testing.T) {
	SetJobStarter(nil)
	SetBackgroundBashEnabled(false)
	t.Cleanup(func() { SetBackgroundBashEnabled(false) })

	var askRes, reportRes ToolResult
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _, _ string, _ SubagentOptions) (string, error) {
			askRes = (&AskTool{}).Run(ctx, map[string]any{"question": "what color?"})
			reportRes = (&ReportProgressTool{}).Run(ctx, map[string]any{"text": "halfway"})
			return "done", nil
		},
	}}

	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "do it"})
	if !res.Success {
		t.Fatalf("subagent call failed: %s", res.Error)
	}
	if askRes.Success || !strings.Contains(askRes.Error, "no job id") {
		t.Fatalf("expected ask to fail with 'no job id', got: %+v", askRes)
	}
	if reportRes.Success || !strings.Contains(reportRes.Error, "no job id") {
		t.Fatalf("expected report_progress to fail with 'no job id', got: %+v", reportRes)
	}
}

// TestCancelRemainingAllAlreadyFinished is review finding 7's first
// uncovered branch: every child already finished by the time cancelRemaining
// runs (the race between ctx firing and the loop reaching each task).
// Constructed directly rather than through a real timing race — hitting
// this window via real goroutine scheduling would be inherently flaky —
// so this exercises cancelRemaining's own logic deterministically.
func TestCancelRemainingAllAlreadyFinished(t *testing.T) {
	st := &spawnedTask{done: make(chan struct{})}
	st.finish(subagentResult{Content: "already done", Success: true})
	close(st.done)

	tool := &SubagentTool{}
	res := tool.cancelRemaining([]*spawnedTask{st})
	if !res.Success || res.Content != "already done" {
		t.Fatalf("expected the already-finished result to pass through untouched, got: %+v", res)
	}
}

// TestCancelRemainingMixedFinishedAndCancelled is review finding 7's second
// uncovered branch: some children already finished, others are still
// running and get cancelled. This is the branch that decides whether
// partial work is surfaced or silently discarded, so it gets its own
// assertions on both halves — the finished task's result must appear in
// the error text, and the still-running task's cancel func must actually
// be invoked (not just counted).
func TestCancelRemainingMixedFinishedAndCancelled(t *testing.T) {
	finishedSt := &spawnedTask{done: make(chan struct{})}
	finishedSt.finish(subagentResult{Content: "fast result", Success: true})
	close(finishedSt.done)

	var cancelCalled atomic.Bool
	stopStream := &atomic.Bool{}
	runningSt := &spawnedTask{
		done:       make(chan struct{}),
		cancel:     func() { cancelCalled.Store(true) },
		stopStream: stopStream,
	}

	tool := &SubagentTool{}
	res := tool.cancelRemaining([]*spawnedTask{finishedSt, runningSt})

	if res.Success {
		t.Fatalf("expected failure when a child was still running, got success: %+v", res)
	}
	if !strings.Contains(res.Error, "1 child(ren) still running") || !strings.Contains(res.Error, "1 already finished") {
		t.Fatalf("expected the counts in the error, got: %q", res.Error)
	}
	if !strings.Contains(res.Error, "fast result") {
		t.Fatalf("expected the finished child's result to be surfaced, got: %q", res.Error)
	}
	if !cancelCalled.Load() {
		t.Fatal("expected the still-running child's cancel func to be called")
	}
	if !stopStream.Load() {
		t.Fatal("expected the still-running child's stopStream to be flipped, same as handOff does")
	}
}

// TestCancelRemainingNoneFinished is the third branch — everything is still
// running and gets cancelled, none finished. Already exercised end-to-end
// by TestPrintModeParentCancelStopsWaitingImmediately; this pins the exact
// message shape directly.
func TestCancelRemainingNoneFinished(t *testing.T) {
	var cancelCalled atomic.Bool
	runningSt := &spawnedTask{
		done:   make(chan struct{}),
		cancel: func() { cancelCalled.Store(true) },
	}

	tool := &SubagentTool{}
	res := tool.cancelRemaining([]*spawnedTask{runningSt})

	if res.Success {
		t.Fatalf("expected failure, got success: %+v", res)
	}
	if !strings.Contains(res.Error, "before any child finished") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
	if !cancelCalled.Load() {
		t.Fatal("expected the still-running child's cancel func to be called")
	}
}
