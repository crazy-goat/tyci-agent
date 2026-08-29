package tools

import (
	"context"
	"testing"
)

func TestDepthFromContext_DefaultsToZero(t *testing.T) {
	if got := DepthFromContext(context.Background()); got != 0 {
		t.Errorf("expected top level (no depth set) to read back as depth 0, got %d", got)
	}
}

func TestWithDepth_RoundTrips(t *testing.T) {
	ctx := WithDepth(context.Background(), 3)
	if got := DepthFromContext(ctx); got != 3 {
		t.Errorf("expected WithDepth(3) to round-trip through DepthFromContext, got %d", got)
	}
}

// depthCapturingRunner is a minimal SubAgentRunner that records the ctx each
// call actually ran with, so a test can inspect what runSingleTask stashed
// on it (depth, in this file's tests).
type depthCapturingRunner struct {
	gotCtx context.Context
}

func (r *depthCapturingRunner) RunTask(ctx context.Context, task string, model string, opts SubagentOptions) (string, error) {
	r.gotCtx = ctx
	return "ok", nil
}

func (r *depthCapturingRunner) RunTaskWithSystem(ctx context.Context, task, model, system string, opts SubagentOptions) (string, error) {
	r.gotCtx = ctx
	return "ok", nil
}

// TestRunSingleTask_DepthPropagation_TopLevelChildIsDepth1 pins the
// top-level half of item 21's depth rule: nothing set on ctx (the top-level
// conversation) is depth 0, so a subagent it spawns lands at depth 1.
func TestRunSingleTask_DepthPropagation_TopLevelChildIsDepth1(t *testing.T) {
	r := &depthCapturingRunner{}
	runSingleTask(context.Background(), r, subagentTask{Task: "x"}, 0, true)
	if r.gotCtx == nil {
		t.Fatal("runner was never called")
	}
	if got := DepthFromContext(r.gotCtx); got != 1 {
		t.Errorf("expected a top-level (depth 0) caller's child to land at depth 1, got %d", got)
	}
}

// TestRunSingleTask_DepthPropagation_IncrementsFromCallerDepth pins the
// general case: a child's depth is always the CALLER's own depth plus one,
// not a fixed constant — this is what lets a chain of scouts (depth 1, 2,
// 3) each see their own child one level deeper.
func TestRunSingleTask_DepthPropagation_IncrementsFromCallerDepth(t *testing.T) {
	r := &depthCapturingRunner{}
	callerCtx := WithDepth(context.Background(), 2)
	runSingleTask(callerCtx, r, subagentTask{Task: "x"}, 0, true)
	if r.gotCtx == nil {
		t.Fatal("runner was never called")
	}
	if got := DepthFromContext(r.gotCtx); got != 3 {
		t.Errorf("expected a depth-2 caller's child to land at depth 3, got %d", got)
	}
}
