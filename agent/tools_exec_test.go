package agent

import (
	"context"
	"testing"

	"github.com/decodo/tyci/stream"
)

// deadlineCapturingRunner records whether the ctx runToolCall handed it had
// a deadline, so tests can assert on the per-tool timeout switch without
// actually waiting out any timeout.
type deadlineCapturingRunner struct {
	gotDeadline bool
}

func (r *deadlineCapturingRunner) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	_, r.gotDeadline = ctx.Deadline()
	return "", nil
}

// TestRunToolCall_LockAndUnlockGetNoExternalTimeout is the regression test
// for a bug where "lock"/"unlock" fell through runToolCall's switch to the
// 60s default: a lock acquired without an explicit "seconds" is documented
// to live until released or the session ends (locks.Registry.Acquire ties
// that to ctx.Done()), but the 60s default silently rebound that lifetime
// to this per-call ctx instead, so every no-TTL lock auto-expired 60s after
// being acquired regardless of caller intent.
func TestRunToolCall_LockAndUnlockGetNoExternalTimeout(t *testing.T) {
	for _, name := range []string{"lock", "unlock"} {
		r := &deadlineCapturingRunner{}
		runToolCall(context.Background(), r, stream.ToolCall{Name: name, Arguments: "{}"}, 0)
		if r.gotDeadline {
			t.Errorf("%s: expected no per-call deadline imposed on ctx, got one", name)
		}
	}
}

// TestRunToolCall_DefaultToolGetsExternalTimeout confirms ordinary tools
// still get the 60s default deadline — the lock/unlock case above is an
// exception, not a change to the fallback behavior.
func TestRunToolCall_DefaultToolGetsExternalTimeout(t *testing.T) {
	r := &deadlineCapturingRunner{}
	runToolCall(context.Background(), r, stream.ToolCall{Name: "todo", Arguments: "{}"}, 0)
	if !r.gotDeadline {
		t.Error("expected a default tool to get a per-call deadline")
	}
}

// TestRunToolCall_SubagentAndWaitGetNoExternalTimeout locks in the existing
// (already correct) exceptions alongside the new lock/unlock ones, so a
// future edit to this switch can't silently regress any of them together.
func TestRunToolCall_SubagentAndWaitGetNoExternalTimeout(t *testing.T) {
	for _, name := range []string{"subagent", "wait"} {
		r := &deadlineCapturingRunner{}
		runToolCall(context.Background(), r, stream.ToolCall{Name: name, Arguments: "{}"}, 0)
		if r.gotDeadline {
			t.Errorf("%s: expected no per-call deadline imposed on ctx, got one", name)
		}
	}
}
