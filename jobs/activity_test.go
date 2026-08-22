package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestSnapshot_FreshJobIdleSinceStart guards item 25's initial-render
// contract: a job that has done absolutely nothing yet must read as "idle
// since start" (a small, correct duration close to zero) rather than a
// zero-value LastActivity that would make the display layer compute a
// nonsense multi-decade duration. Revert the lastActivity seeding in
// Registry.Start and this fails because Snapshot().LastActivity comes back
// as the zero time.Time.
func TestSnapshot_FreshJobIdleSinceStart(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job := r.Start(context.Background(), "demo", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "", false, nil
	})
	defer close(release)

	snap := job.Snapshot()
	if snap.LastActivity.IsZero() {
		t.Fatalf("expected a fresh job's LastActivity to be seeded at Start, got zero value")
	}
	if since := time.Since(snap.LastActivity); since < 0 || since > time.Second {
		t.Fatalf("expected a fresh job's LastActivity to read as ~idle-since-start, got %s ago", since)
	}
}

// TestTouchActivity_UpdatesSnapshot guards the core mechanism: a call to
// Registry.TouchActivity must be visible on the NEXT Snapshot(), materialized
// into the returned copy's LastActivity field. Revert TouchActivity (or its
// wiring into Snapshot) and this fails because LastActivity never advances
// past its Start-time seed.
func TestTouchActivity_UpdatesSnapshot(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job := r.Start(context.Background(), "demo", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "", false, nil
	})
	defer close(release)

	before := job.Snapshot().LastActivity
	time.Sleep(5 * time.Millisecond)
	r.TouchActivity(job.ID)
	after := job.Snapshot().LastActivity

	if !after.After(before) {
		t.Fatalf("expected LastActivity to advance after TouchActivity, before=%s after=%s", before, after)
	}
}

// TestTouchActivity_UnknownIDIsNoop guards against TouchActivity panicking
// or otherwise misbehaving for an id the registry doesn't know about — it
// must be a silent no-op, the same contract SetProgress documents for an
// unknown id.
func TestTouchActivity_UnknownIDIsNoop(t *testing.T) {
	r := NewRegistry()
	r.TouchActivity("no-such-job") // must not panic
}

// TestTouchActivity_NeverGoesBackward guards the monotonicity requirement:
// touchActivity must never leave lastActivity earlier than an
// already-recorded value, even when its own "now" reads earlier than what's
// already stored (clock jitter, or a stale call racing a newer one). This
// pins a synthetic future value directly into the unexported field (this
// test is in-package) and then calls touchActivity with the real, earlier
// "now" — a plain unconditional store (no CAS-and-compare) would let that
// earlier call clobber the future value, which this test catches. Revert
// the CAS loop in Job.touchActivity to an unconditional atomic.StoreInt64
// and this fails.
func TestTouchActivity_NeverGoesBackward(t *testing.T) {
	j := &Job{}
	future := time.Now().Add(time.Hour).UnixNano()
	atomic.StoreInt64(&j.lastActivity, future)

	j.touchActivity() // real "now" is earlier than the pinned future value

	if got := atomic.LoadInt64(&j.lastActivity); got != future {
		t.Fatalf("expected touchActivity to leave a later-than-now value untouched, got %d want %d", got, future)
	}
}

// TestTouchActivity_AdvancesMonotonicallyInThePlainCase guards the ordinary,
// non-adversarial path: repeated real touches must never decrease
// LastActivity as observed through Snapshot.
func TestTouchActivity_AdvancesMonotonicallyInThePlainCase(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job := r.Start(context.Background(), "demo", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "", false, nil
	})
	defer close(release)

	var last time.Time
	for i := 0; i < 5; i++ {
		r.TouchActivity(job.ID)
		got := job.Snapshot().LastActivity
		if got.Before(last) {
			t.Fatalf("touch %d: LastActivity went backward: %s before %s", i, got, last)
		}
		last = got
		time.Sleep(time.Millisecond)
	}
}

// TestSnapshot_LastActivityPersistsPastCompletion guards against
// LastActivity being cleared on job completion — like Progress, it must
// persist past terminal state so a completed job's last known sign of life
// is still visible (feeds item 28's "went quiet before it failed"
// diagnosis). Revert to clearing it on completion and this fails because
// LastActivity comes back zero after Wait.
func TestSnapshot_LastActivityPersistsPastCompletion(t *testing.T) {
	r := NewRegistry()
	job := r.Start(context.Background(), "demo", func(ctx context.Context, jobID string) (string, bool, error) {
		r.TouchActivity(jobID)
		return "done", false, nil
	})

	final, ok := r.Wait(context.Background(), job.ID, time.Second)
	if !ok || final.Status != StatusDone {
		t.Fatalf("expected job to finish, got ok=%v status=%v", ok, final)
	}
	if final.LastActivity.IsZero() {
		t.Fatalf("expected LastActivity to survive completion, got zero value")
	}
}
