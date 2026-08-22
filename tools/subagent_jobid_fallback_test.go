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
// ask.go) — a `tyci run` invocation never drains JobNotices and the
// blocking "subagent" tool call here never returns until every child
// finishes, so nobody is ever free to call "answer_job".

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// printModeEnv wires a real job registry the same way main() does, but
// deliberately leaves background bash (and therefore backgroundAllowed)
// off — the one flag `tyci run` / `--print` never turns on. jobs.Registry
// itself satisfies JobAsker/JobAnswerer/JobProgressReporter structurally
// (same shapes, no adapter needed), same as testJobStarter wraps it for
// JobStarter.
func printModeEnv(t *testing.T) (*jobs.Registry, *recordingNotifier) {
	t.Helper()
	reg := jobs.NewRegistry()
	notifier := &recordingNotifier{}
	SetJobStarter(testJobStarter{reg})
	SetJobAsker(reg)
	SetJobAnswerer(reg)
	SetJobProgressReporter(reg)
	SetJobNotifier(notifier)
	SetBackgroundBashEnabled(false)
	t.Cleanup(func() {
		SetJobStarter(nil)
		SetJobAsker(nil)
		SetJobAnswerer(nil)
		SetJobProgressReporter(nil)
		SetJobNotifier(nil)
		SetBackgroundBashEnabled(false)
	})
	return reg, notifier
}

// TestPrintModeChildIsStillRegisteredAsAJob is the core of item 17: a
// blocking subagent call in a mode with no handoff (jobStarter wired,
// backgroundAllowed false) must still register its child through
// jobStarter — not silently fall back to the old no-job-id runTasks path —
// even though it never hands the child to the background.
func TestPrintModeChildIsStillRegisteredAsAJob(t *testing.T) {
	reg, _ := printModeEnv(t)
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(context.Context, string, string, SubagentOptions) (string, error) {
			return "the answer", nil
		},
	}}

	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "do it"})
	if !res.Success || res.Content != "the answer" {
		t.Fatalf("unexpected result: %+v", res)
	}

	regJobs := reg.List()
	if len(regJobs) != 1 {
		t.Fatalf("expected the child to be registered as exactly one job, got %d: %+v", len(regJobs), regJobs)
	}
	job := waitTerminal(t, reg, regJobs[0].ID)
	if job.Status != jobs.StatusDone {
		t.Fatalf("expected the job to finish done, got %s: %+v", job.Status, job)
	}
	if job.Result != "the answer" {
		t.Fatalf("expected job.Result to carry the child's answer, got %q", job.Result)
	}
}

// TestPrintModeChildCanReportProgress guards the same fix from
// report_progress's side: it needs a job id, which this mode did not
// provide before. The tool.Run call above only returns once every spawned
// child's job.done has fired (spawn's finish() runs before that close, see
// waitTerminal's doc comment), so reading reportRes without extra
// synchronization here is safe — that ordering is what makes it safe.
func TestPrintModeChildCanReportProgress(t *testing.T) {
	_, _ = printModeEnv(t)
	var reportRes ToolResult
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _, _ string, _ SubagentOptions) (string, error) {
			reportRes = (&ReportProgressTool{}).Run(ctx, map[string]any{"text": "halfway"})
			return "done", nil
		},
	}}

	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "do it"})
	if !res.Success {
		t.Fatalf("subagent call failed: %s", res.Error)
	}
	if !reportRes.Success {
		t.Fatalf("report_progress failed inside a print-mode child: %s", reportRes.Error)
	}
}

// TestAskFailsFastWhenTheSpawningCallCannotHandOff is blocker (a) from
// review: giving these children a job id must not make "ask_parent" pass its
// job-id gate and then block for its whole timeout on the one path where an
// answer can structurally never arrive (the "subagent" tool call here does
// not return until the child itself finishes — there is no handoff to free
// it up, and no drained JobNotices either). It must fail immediately,
// exactly as it did before this job id existed at all.
func TestAskFailsFastWhenTheSpawningCallCannotHandOff(t *testing.T) {
	_, _ = printModeEnv(t)
	var askRes ToolResult
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _, _ string, _ SubagentOptions) (string, error) {
			askRes = (&AskTool{}).Run(ctx, map[string]any{"question": "what color?"})
			return "done", nil
		},
	}}

	done := make(chan struct{})
	go func() {
		tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "ask something"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ask blocked instead of failing fast — nobody in this mode could ever answer it")
	}

	if askRes.Success {
		t.Fatalf("expected ask to fail fast, got success: %+v", askRes)
	}
	if !strings.Contains(askRes.Error, "cannot get an answer") {
		t.Fatalf("unexpected ask error: %q", askRes.Error)
	}
}

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

// TestPrintModeChildRunsExactlyOnce guards against the exact historical bug
// item 20 already fixed for the handoff path (TestBlockingSingleTaskRunsExactlyOnce):
// a fall-through that runs the child via the new registered path AND THEN
// falls through to the old plain runTasks would double the model calls,
// double any side effects, and register one job while actually running the
// task twice. It must run exactly once.
func TestPrintModeChildRunsExactlyOnce(t *testing.T) {
	reg, _ := printModeEnv(t)
	var calls atomic.Int32
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(context.Context, string, string, SubagentOptions) (string, error) {
			calls.Add(1)
			return "the answer", nil
		},
	}}

	res := tool.Run(ctxWithParentModel("test/model"), map[string]any{"task": "do it"})
	if !res.Success || res.Content != "the answer" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("runner invoked %d times, want exactly 1", n)
	}
	if n := len(reg.List()); n != 1 {
		t.Fatalf("expected exactly one registered job, got %d", n)
	}
}

// TestPrintModeParentCancelStopsWaitingImmediately is the regression test
// for review finding 1: cancelling the parent ctx must interrupt this
// path's wait right away, not leave it sitting on <-st.done for as long as
// the child runs (up to SubagentTimeoutSec). Before the fix, the wait loop
// here had no ctx.Done() arm at all — spawn's jobCtx is detached from ctx
// via context.WithoutCancel specifically so a legitimately backgrounded
// child survives its tool call returning, so nothing else would have
// stopped this child either.
//
// Run returning promptly is necessary but NOT sufficient: cancelRemaining
// could return right away without ever actually stopping the child, leaving
// it to keep calling the model and writing files until its own
// SubagentTimeoutSec backstop — invisible to a test that only times how
// long Run takes. So this also asserts the child's own context really was
// cancelled (via childCtxCancelled, set only on ctx.Done() inside the
// runner) and that its job ends up StatusFailed in the registry, not left
// StatusRunning.
func TestPrintModeParentCancelStopsWaitingImmediately(t *testing.T) {
	reg, notifier := printModeEnv(t)
	release := make(chan struct{})
	defer close(release)

	var childCtxCancelled atomic.Bool
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _, _ string, _ SubagentOptions) (string, error) {
			select {
			case <-release:
				return "finished before cancel", nil
			case <-ctx.Done():
				childCtxCancelled.Store(true)
				return "", ctx.Err()
			}
		},
	}}

	ctx, cancel := context.WithCancel(ctxWithParentModel("test/model"))
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	done := make(chan ToolResult, 1)
	go func() {
		done <- tool.Run(ctx, map[string]any{"task": "slow"})
	}()

	select {
	case res := <-done:
		if res.Success {
			t.Fatalf("expected the cancelled call to report failure, got success: %+v", res)
		}
		if !strings.Contains(res.Error, "cancelled") {
			t.Fatalf("expected the error to mention the cancellation, got: %q", res.Error)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return promptly after the parent ctx was cancelled — it is still waiting on the child")
	}

	regJobs := reg.List()
	if len(regJobs) != 1 {
		t.Fatalf("expected exactly one registered job, got %d", len(regJobs))
	}
	job := waitTerminal(t, reg, regJobs[0].ID)
	if job.Status != jobs.StatusFailed {
		t.Fatalf("expected the cancelled child's job to end failed, got %s: %+v", job.Status, job)
	}
	if !childCtxCancelled.Load() {
		t.Fatal("the child's own context was never cancelled — st.cancel() did not actually run")
	}

	// Review finding 2: a cancelled child must NOT emit the
	// background-finished/FAILED notice — nothing in this mode drains
	// JobNotices, and the notice text invites wait(job_id=...) where no id
	// was ever surfaced to the model. waitTerminal above already guarantees
	// the child's goroutine (and any notify() call inside it) has run.
	if got := notifier.all(); len(got) != 0 {
		t.Fatalf("expected no completion notice for a cancelled child, got: %v", got)
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
