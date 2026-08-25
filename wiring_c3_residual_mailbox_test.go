package main

// C3 (batch-2 review round 1): jobs.Registry.Post reports success for any
// live job, but "live" only means the job's OWN agent loop might still
// drain its mailbox at its next iteration boundary — a job whose final
// iteration has already happened (agent.Run does not drain after its last
// turn) will never read another posted message again, and the mailbox is
// gone entirely once the job is pruned. Before jobs.Job.ResidualMailbox
// existed, that content — including a notice notifyToParent/main.go's
// onEvent hook had routed there — simply vanished with no trace once the
// job finished; before notice routing was addressed at all, the same
// content would at least have appeared (wrongly addressed, but visible)
// on the main queue. This test pins the fix: whatever is still sitting in
// a job's mailbox when it goes terminal is swept to the main queue,
// tagged, instead of disappearing.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

func TestWiring_C3_ResidualMailboxSweptToMainOnCompletion(t *testing.T) {
	reg, _ := withTestWiring(t)

	release := make(chan struct{})
	fork := reg.Start(context.Background(), "fork", jobs.KindSubagent, "", func(ctx context.Context, jobID string) (string, bool, error) {
		<-release
		// Deliberately never drains its own mailbox — standing in for a
		// fork whose last agent-loop iteration has already happened by
		// the time something posts to it (agent.Run does not drain after
		// its final iteration — see agent/agent.go).
		return "fork done", false, nil
	})

	if !reg.Post(fork.ID, "a message that will never be drained") {
		t.Fatal("expected Post to succeed against a live job")
	}

	close(release)
	final, ok := reg.Wait(context.Background(), fork.ID, 2*time.Second)
	if !ok || final.Status != jobs.StatusDone {
		t.Fatalf("expected fork to finish as done, got ok=%v status=%v", ok, final)
	}

	// Accumulate, then settle and drain once more, for the same reason the
	// capped sibling below has to: Drain removes what it returns, so a
	// break-at-first-non-empty poll asserts on whatever happened to have
	// arrived by then. Here that mattered less (one message means one
	// Notify, so no partial batch) but the "exactly once" claim is only
	// real if nothing further arrives afterwards — without the settle, a
	// sweep that forwarded the same message twice would still pass.
	deadline := time.Now().Add(2 * time.Second)
	var pending []string
	for time.Now().Before(deadline) {
		pending = append(pending, JobNotices.Drain()...)
		if len(pending) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	pending = append(pending, JobNotices.Drain()...)

	if len(pending) != 1 {
		t.Fatalf("expected the residual mailbox message to be swept to main exactly once, got %v", pending)
	}
	if !strings.Contains(pending[0], fork.ID) {
		t.Fatalf("expected the forwarded notice to name the finished job, got %q", pending[0])
	}
	if !strings.Contains(pending[0], "a message that will never be drained") {
		t.Fatalf("expected the forwarded notice to carry the original text, got %q", pending[0])
	}
}

// TestWiring_C3_DrainedMailboxIsNotSweptTwice: a message the job DID drain
// (via its own NextMessages, mirroring normal delivery) before finishing
// must not also show up on the main queue — the sweep only picks up what
// is left behind, not everything a job ever received.
func TestWiring_C3_DrainedMailboxIsNotSweptTwice(t *testing.T) {
	reg, _ := withTestWiring(t)

	posted := make(chan struct{})
	drained := make(chan struct{})
	job := reg.Start(context.Background(), "drainer", jobs.KindSubagent, "", func(ctx context.Context, jobID string) (string, bool, error) {
		<-posted
		// Drain it itself, as a real agent loop's NextMessages would.
		msgs := reg.DrainMessages(jobID)
		if len(msgs) != 1 {
			t.Errorf("expected to drain exactly one message, got %v", msgs)
		}
		close(drained)
		return "done", false, nil
	})

	if !reg.Post(job.ID, "a message the job drains itself") {
		t.Fatal("expected Post to succeed against a live job")
	}
	close(posted)

	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("job never drained its mailbox")
	}

	final, ok := reg.Wait(context.Background(), job.ID, 2*time.Second)
	if !ok || final.Status != jobs.StatusDone {
		t.Fatalf("expected job to finish as done, got ok=%v status=%v", ok, final)
	}

	time.Sleep(50 * time.Millisecond)
	if pending := JobNotices.Drain(); len(pending) != 0 {
		t.Fatalf("expected nothing swept to main for a message the job already drained itself, got %v", pending)
	}
}

// TestWiring_D5_ResidualMailboxSweepIsCapped: an orphaned job with more
// residual mailbox entries than residualMailboxSweepCap must not dump all
// of them into main individually — past the cap, the remainder is
// summarized as a count instead of shown one by one.
func TestWiring_D5_ResidualMailboxSweepIsCapped(t *testing.T) {
	reg, _ := withTestWiring(t)

	release := make(chan struct{})
	fork := reg.Start(context.Background(), "fork", jobs.KindSubagent, "", func(ctx context.Context, jobID string) (string, bool, error) {
		<-release
		return "fork done", false, nil
	})

	total := residualMailboxSweepCap + 3
	for i := 0; i < total; i++ {
		if !reg.Post(fork.ID, fmt.Sprintf("message %d", i)) {
			t.Fatalf("expected Post %d to succeed against a live job", i)
		}
	}

	close(release)
	final, ok := reg.Wait(context.Background(), fork.ID, 2*time.Second)
	if !ok || final.Status != jobs.StatusDone {
		t.Fatalf("expected fork to finish as done, got ok=%v status=%v", ok, final)
	}

	// ACCUMULATE across drains, and wait for the full expected count rather
	// than breaking at the first non-empty drain. The sweep calls
	// notices.Notify once PER message (main.go's onEvent hook), so a drain
	// that lands mid-loop legitimately returns a partial batch — and Drain
	// removes what it returns, so a partial first drain used to throw the
	// rest away and the test then failed on the count. That is exactly how
	// this test came to fail 4 runs out of 5 on main: not a flake and not a
	// defect in the sweep, but a test that raced its own subject. The
	// single-message sibling above never noticed, because with one Notify
	// there is no partial batch to catch.
	want := residualMailboxSweepCap + 1
	deadline := time.Now().Add(2 * time.Second)
	var pending []string
	for time.Now().Before(deadline) {
		pending = append(pending, JobNotices.Drain()...)
		if len(pending) >= want {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Settle, then drain once more: reaching `want` is not proof the sweep
	// stopped there. Without this, a sweep that wrongly emitted every
	// message individually would still pass as soon as it crossed the
	// expected count, which is the whole property this test exists to pin.
	time.Sleep(50 * time.Millisecond)
	pending = append(pending, JobNotices.Drain()...)

	// residualMailboxSweepCap shown individually, plus exactly one summary
	// line for the rest.
	if len(pending) != want {
		t.Fatalf("expected %d notices (cap + one summary), got %d: %v", want, len(pending), pending)
	}
	summary := pending[len(pending)-1]
	remaining := total - residualMailboxSweepCap
	if !strings.Contains(summary, fmt.Sprintf("%d more", remaining)) {
		t.Fatalf("expected the summary line to mention %d more, got %q", remaining, summary)
	}
	if !strings.Contains(summary, "not delivered") {
		t.Fatalf("expected the summary line to say the rest were not delivered, got %q", summary)
	}
}
