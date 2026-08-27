package jobs

// TestNeedsProgressHeartbeat_* pins item 15's periodic report_progress
// nudge: a running subagent job that has gone quiet for longer than a given
// duration should be nudged exactly once per interval, never on every call,
// and never for a job that has already reported, is not running, or does
// not exist.

import (
	"context"
	"testing"
	"time"
)

// blockingJobFn runs a job that blocks on release, letting the test control
// exactly how long the job has been "running" before checking the
// heartbeat.
func blockingJobFn(release <-chan struct{}) func(ctx context.Context, jobID string) (string, bool, error) {
	return func(ctx context.Context, jobID string) (string, bool, error) {
		<-release
		return "done", false, nil
	}
}

// TestNeedsProgressHeartbeat_FiresAfterThreshold_NotBefore pins the basic
// time gate: a job that has been running less than `after` is not nudged;
// once `after` has elapsed with no report_progress call, it is.
func TestNeedsProgressHeartbeat_FiresAfterThreshold_NotBefore(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)

	job := r.Start(context.Background(), "quiet child", KindSubagent, "", blockingJobFn(release))

	const after = 30 * time.Millisecond
	if r.NeedsProgressHeartbeat(job.ID, after) {
		t.Fatal("expected no heartbeat nudge immediately after start")
	}

	time.Sleep(after + 10*time.Millisecond)
	if !r.NeedsProgressHeartbeat(job.ID, after) {
		t.Fatal("expected a heartbeat nudge once the job has been quiet past the threshold")
	}
}

// TestNeedsProgressHeartbeat_DoesNotRepeatEveryCall pins the exact trap
// item 15's spec calls out: once a nudge has fired, calling
// NeedsProgressHeartbeat again immediately afterward — as the agent loop
// would on every subsequent iteration — must NOT fire again until another
// full interval has passed. Without lastHeartbeatNudgeAt tracking this, a
// child that never calls report_progress would be nagged on every single
// loop iteration forever.
func TestNeedsProgressHeartbeat_DoesNotRepeatEveryCall(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)

	job := r.Start(context.Background(), "quiet child", KindSubagent, "", blockingJobFn(release))

	const after = 30 * time.Millisecond
	time.Sleep(after + 10*time.Millisecond)

	if !r.NeedsProgressHeartbeat(job.ID, after) {
		t.Fatal("expected the first call past the threshold to fire")
	}
	// Immediately re-checking, simulating the very next loop iteration:
	// must not fire again right away.
	for i := 0; i < 5; i++ {
		if r.NeedsProgressHeartbeat(job.ID, after) {
			t.Fatalf("call %d: expected no repeat nudge immediately after one already fired", i)
		}
	}

	// After another full interval with still no report_progress call, it
	// should be willing to nudge again.
	time.Sleep(after + 10*time.Millisecond)
	if !r.NeedsProgressHeartbeat(job.ID, after) {
		t.Fatal("expected a second nudge after another full interval elapsed")
	}
}

// TestNeedsProgressHeartbeat_RealProgressResetsTimer pins that an actual
// report_progress call (SetProgress) resets the quiet timer just like a
// nudge does — a child that IS reporting must never be nagged.
func TestNeedsProgressHeartbeat_RealProgressResetsTimer(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)

	job := r.Start(context.Background(), "chatty child", KindSubagent, "", blockingJobFn(release))

	const after = 40 * time.Millisecond
	time.Sleep(after / 2)
	if ok := r.SetProgress(job.ID, "halfway there"); !ok {
		t.Fatal("expected SetProgress to succeed for a running job")
	}

	// Total elapsed since Start now exceeds `after`, but elapsed since the
	// report_progress call above does not — must not fire yet.
	time.Sleep(after/2 + 5*time.Millisecond)
	if r.NeedsProgressHeartbeat(job.ID, after) {
		t.Fatal("expected no nudge: report_progress reset the quiet timer more recently than `after` ago")
	}

	time.Sleep(after)
	if !r.NeedsProgressHeartbeat(job.ID, after) {
		t.Fatal("expected a nudge once the job has been quiet, since the report_progress call, past the threshold")
	}
}

// TestNeedsProgressHeartbeat_NotRunning_NeverFires pins that a job which is
// not currently running (waiting-for-answer or terminal) is never nudged —
// the parent already has a different, more direct signal for a blocked
// child (the ask/pending-jobs reminder), and a terminal job has nothing
// left to report.
func TestNeedsProgressHeartbeat_NotRunning_NeverFires(t *testing.T) {
	r := NewRegistry()

	job := r.Start(context.Background(), "quick child", KindSubagent, "", func(ctx context.Context, jobID string) (string, bool, error) {
		return "done", false, nil
	})
	if _, ok := r.Wait(context.Background(), job.ID, time.Second); !ok {
		t.Fatal("timed out waiting for job to finish")
	}

	if r.NeedsProgressHeartbeat(job.ID, time.Nanosecond) {
		t.Fatal("expected no nudge for a job that has already finished")
	}
}

// TestNeedsProgressHeartbeat_WaitingForAnswer_NeverFires pins the other
// non-running status: a job blocked in Ask (StatusWaitingAnswer) is already
// a dead end only the parent's answer_job can unblock — a report_progress
// nudge would not help it, and per NeedsProgressHeartbeat's doc comment this
// status is deliberately excluded the same way a terminal job is.
func TestNeedsProgressHeartbeat_WaitingForAnswer_NeverFires(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)

	askStarted := make(chan struct{})
	job := r.Start(context.Background(), "asking child", KindSubagent, "", func(ctx context.Context, jobID string) (string, bool, error) {
		close(askStarted)
		_, _, _ = r.Ask(ctx, jobID, "what now?")
		<-release
		return "done", false, nil
	})

	<-askStarted
	deadline := time.Now().Add(2 * time.Second)
	for {
		reachedWaiting := false
		for _, snap := range r.List() {
			if snap.ID == job.ID && snap.Status == StatusWaitingAnswer {
				reachedWaiting = true
				break
			}
		}
		if reachedWaiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the job to reach StatusWaitingAnswer")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if r.NeedsProgressHeartbeat(job.ID, time.Nanosecond) {
		t.Fatal("expected no nudge for a job that is blocked waiting for an answer")
	}
}

// TestNeedsProgressHeartbeat_UnknownJob_ReturnsFalse pins the not-found case.
func TestNeedsProgressHeartbeat_UnknownJob_ReturnsFalse(t *testing.T) {
	r := NewRegistry()
	if r.NeedsProgressHeartbeat("no-such-job", time.Nanosecond) {
		t.Fatal("expected false for an unknown job id")
	}
}

// TestNeedsProgressHeartbeat_NonPositiveAfter_NeverFires pins the guard
// against a zero or negative threshold, which would otherwise fire on the
// very first call for any running job.
func TestNeedsProgressHeartbeat_NonPositiveAfter_NeverFires(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)
	job := r.Start(context.Background(), "child", KindSubagent, "", blockingJobFn(release))

	if r.NeedsProgressHeartbeat(job.ID, 0) {
		t.Fatal("expected false for a zero threshold")
	}
	if r.NeedsProgressHeartbeat(job.ID, -time.Second) {
		t.Fatal("expected false for a negative threshold")
	}
}
