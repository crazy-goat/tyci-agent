package jobs

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// TestStartFinalizesJobOnGoexit is F4: a job function that calls
// runtime.Goexit() (as testing.T.FailNow does, deep in an unrelated call
// chain) must still leave the job terminal — StatusFailed, FinishedAt set,
// job.done closed, and Wait unblocked — rather than stuck in StatusRunning
// forever with Wait hanging.
//
// Revert check: before this fix, Start's goroutine ran cancel() and the
// registry's completion bookkeeping as plain sequential code AFTER the
// inner recover-guarded call to fn. runtime.Goexit() unwinds the entire
// goroutine's stack running only DEFERRED calls; it skips ordinary
// sequential code in every frame it passes through. So on the pre-fix code
// this test's Wait call times out (job never leaves StatusRunning) instead
// of observing StatusFailed.
func TestStartFinalizesJobOnGoexit(t *testing.T) {
	r := NewRegistry()
	started := make(chan struct{})
	job := r.Start(context.Background(), "goexits", KindSubagent, "", func(ctx context.Context, id string) (string, bool, error) {
		close(started)
		runtime.Goexit()
		// unreachable
		return "unreachable", false, nil
	})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not start")
	}

	snap, ok := r.Wait(context.Background(), job.ID, 2*time.Second)
	if !ok {
		t.Fatal("Wait timed out: job never became terminal after runtime.Goexit()")
	}
	if snap.Status != StatusFailed {
		t.Fatalf("expected StatusFailed after Goexit, got %v (err=%q)", snap.Status, snap.Err)
	}
	if snap.Err == "" {
		t.Fatal("expected a non-empty error describing the Goexit, got empty")
	}
	if snap.FinishedAt.IsZero() {
		t.Fatal("expected FinishedAt to be set")
	}

	select {
	case <-job.done:
	default:
		t.Fatal("job.done was never closed")
	}
}
