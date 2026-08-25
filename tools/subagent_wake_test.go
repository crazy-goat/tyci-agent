package tools

// B5 (batch-2 audit): runWithHandoff's blocking-call select used to be
// blind to a spawned child entering StatusWaitingAnswer — it only noticed a
// blocked child indirectly, by sitting out the rest of SubagentBackgroundAfterSec
// (or the whole call, without handoff) before the handoff even let the
// parent see the queued ask-notice. These tests assert on the wake itself
// (a channel closing / a goroutine unblocking), not on wall-clock luck.

import (
	"context"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// realJobWaiter wraps a real jobs.Registry, unlike testJobWaiter
// (subagent_test.go), which never sets JobStatus.Waiting at all — that gap
// would hide exactly the bug B5 fixes, since runWithHandoff's watcher only
// has something to notice through that field.
type realJobWaiter struct{ reg *jobs.Registry }

func (w realJobWaiter) Wait(ctx context.Context, id string, timeout time.Duration) (JobStatus, bool) {
	job, ok := w.reg.Wait(ctx, id, timeout)
	if !ok {
		return JobStatus{}, false
	}
	return JobStatus{
		ID:       job.ID,
		Done:     job.Status != jobs.StatusRunning && job.Status != jobs.StatusWaitingAnswer,
		Success:  job.Status == jobs.StatusDone || job.Status == jobs.StatusTruncated,
		Content:  job.Result,
		Error:    job.Err,
		Waiting:  job.Status == jobs.StatusWaitingAnswer,
		Question: job.Question,
	}, true
}

// wakeEnv wires a real registry plus a JobWaiter over it (SetJobWaiter),
// mirroring production wiring closely enough that getJobWaiter() (used by
// runWithHandoff's watcher) sees the same one wait() would.
func wakeEnv(t *testing.T) *jobs.Registry {
	t.Helper()
	reg := jobs.NewRegistry()
	SetJobStarter(testJobStarter{reg})
	SetJobWaiter(realJobWaiter{reg})
	SetJobNotifier(&recordingNotifier{})
	SetBackgroundBashEnabled(true)
	t.Cleanup(func() {
		SetJobStarter(nil)
		SetJobWaiter(nil)
		SetJobNotifier(nil)
		SetBackgroundBashEnabled(false)
	})
	return reg
}

// TestWatchForWaiting_WakesWhenAlreadyWaitingBeforeCalled is B5's explicit
// "asks before the select is entered" case, isolated to the exact
// mechanism responsible for it: watchForWaiting's own first call uses
// timeout 0 specifically so a question already pending by the time it
// starts watching is not missed (see its doc comment). The question is
// asked and confirmed pending (via jobs.Registry's onEvent hook — a real
// synchronization point, not a sleep) strictly BEFORE watchForWaiting is
// ever invoked, so there is nothing here for a select to race against: if
// this test is flaky or hangs, the "ask before watching starts" case is
// broken.
func TestWatchForWaiting_WakesWhenAlreadyWaitingBeforeCalled(t *testing.T) {
	reg := jobs.NewRegistry()
	sawWaiting := make(chan struct{})
	closedOnce := false
	reg.SetOnEvent(func(j jobs.Job) {
		if j.Status == jobs.StatusWaitingAnswer && !closedOnce {
			closedOnce = true
			close(sawWaiting)
		}
	})

	job := reg.Start(context.Background(), "asker", jobs.KindSubagent, "", func(ctx context.Context, jobID string) (string, bool, error) {
		_, _, _ = reg.Ask(ctx, jobID, "which branch?")
		return "done", false, nil
	})

	select {
	case <-sawWaiting:
	case <-time.After(2 * time.Second):
		t.Fatal("job never reached StatusWaitingAnswer")
	}

	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		watchForWaiting(context.Background(), realJobWaiter{reg}, job.ID, wake)
		close(done)
	}()

	select {
	case <-wake:
	case <-time.After(2 * time.Second):
		t.Fatal("watchForWaiting did not wake for a question that was already pending before it started watching")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchForWaiting did not return after sending its wake")
	}

	reg.Answer(job.ID, "main", true)
}

// TestRunWithHandoff_WakesWhenChildAsksMidCall is B5's main scenario: a
// child that asks a question shortly after runWithHandoff's select has
// already committed to blocking must make the call return promptly,
// instead of only after SubagentBackgroundAfterSec (here set far longer
// than the assertion's margin, so a pass proves the wake fired — it cannot
// be explained by the timer arm winning instead).
func TestRunWithHandoff_WakesWhenChildAsksMidCall(t *testing.T) {
	reg := wakeEnv(t)
	prevAfter := SubagentBackgroundAfterSec
	SubagentBackgroundAfterSec = 30 * time.Second
	t.Cleanup(func() { SubagentBackgroundAfterSec = prevAfter })

	askedAt100ms := make(chan struct{})
	answered := make(chan struct{})
	tool := &SubagentTool{Runner: &mockRunner{
		RunTaskFunc: func(ctx context.Context, _ string, _ string, _ SubagentOptions) (string, error) {
			jobID, _ := ctx.Value(JobIDCtxKey{}).(string)
			time.Sleep(100 * time.Millisecond)
			close(askedAt100ms)
			answer, _, _ := reg.Ask(ctx, jobID, "which environment?")
			close(answered)
			return "answer was: " + answer, nil
		},
	}}

	start := time.Now()
	resultCh := make(chan struct {
		res    ToolResult
		handed bool
	}, 1)
	go func() {
		res, handed := tool.runWithHandoff(context.Background(), []subagentTask{{Task: "ask something"}}, true)
		resultCh <- struct {
			res    ToolResult
			handed bool
		}{res, handed}
	}()

	select {
	case <-askedAt100ms:
	case <-time.After(2 * time.Second):
		t.Fatal("child never reached the point of asking")
	}

	select {
	case r := <-resultCh:
		elapsed := time.Since(start)
		if !r.handed {
			t.Fatalf("expected the call to hand off once the child asked a question, got inline result: %+v", r.res)
		}
		if !r.res.Success {
			t.Fatalf("handoff result was not success: %+v", r.res)
		}
		// Comfortably below SubagentBackgroundAfterSec (30s): the wake, not
		// the timer, must be what ended this call.
		if elapsed > 5*time.Second {
			t.Fatalf("runWithHandoff took %s to return after the child asked; expected a prompt wake well under the %s handoff timer", elapsed, SubagentBackgroundAfterSec)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithHandoff did not return promptly after the child asked a question")
	}

	// Let the child finish so it does not leak past the test.
	jobs := reg.List()
	if len(jobs) != 1 {
		t.Fatalf("expected exactly one job, got %d", len(jobs))
	}
	if !reg.Answer(jobs[0].ID, "staging", true) {
		t.Fatal("expected Answer to succeed")
	}
	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		t.Fatal("child never received its answer")
	}
}
