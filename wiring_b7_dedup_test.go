package main

// B7 (batch-2 audit): jobs.Registry.Ask fires the onEvent hook (which
// queues a notice) AND a concurrent jobs.Registry.Wait call returns
// status.Waiting with the question text — without deduplication, a single
// question could reach the parent twice in one turn. wireTools's onEvent
// hook (main.go) is made authoritative for "nobody was already waiting";
// a Wait call already blocked on the job when the question is posed
// (jobs.Job.QuestionHasWaiter, set by Ask) is what suppresses the notice in
// the other case, since that caller is about to receive the very same
// question back synchronously.
//
// Review round 1 (C1) found that this only holds for a REPORTING waiter —
// something that hands the question back to whoever called it, like the
// "wait" tool going through jobs.Registry.Wait. A caller that merely
// watches a job (jobs.Registry.WaitObserve — tools/subagent.go's
// runWithHandoff watcher) must NOT suppress the notice, because it does
// not itself deliver the question to anyone; before WaitObserve existed
// and the watcher used Wait, this was reachable on essentially every
// blocking subagent call. TestWiring_B7_ObserverOnly_NoticeNotSuppressed
// below is that regression's coverage.
//
// These tests drive the exact production wiring (wireTools, via
// withTestWiring) rather than reimplementing the dedup decision.

import (
	"context"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// waitUntilRegistered polls jobs.Registry.WaiterCount (a real, synchronized
// count) until a Wait call has registered on id, instead of guessing with
// a fixed sleep — see review round 1: a goroutine that has not yet reached
// Wait's internal statusCh capture would otherwise catch the NEW,
// post-transition channel and block for the full timeout instead of
// waking, if the question were asked before it actually got there.
func waitUntilRegistered(t *testing.T, reg *jobs.Registry, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if reg.WaiterCount(id) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Wait call never registered as a waiter on the job")
		}
		time.Sleep(time.Millisecond)
	}
}

// waitUntilObserverRegistered is waitUntilRegistered's WaitObserve
// counterpart (see jobs.Registry.ObserverCount) — batch-2 review round 2's
// "optional if cheap" suggestion, turning what was a settle sleep into a
// real synchronization point.
func waitUntilObserverRegistered(t *testing.T, reg *jobs.Registry, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if reg.ObserverCount(id) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("WaitObserve call never registered as an observer on the job")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWiring_B7_ReportingWaiter_NoticeSuppressed is the "parent is in
// wait" case, specifically for a REPORTING waiter (jobs.Registry.Wait,
// what the "wait" tool uses): a caller already blocked in Wait for this
// exact job when it asks a question must get exactly one delivery — its
// own Wait call's Waiting result — and the main notice queue must stay
// empty.
func TestWiring_B7_ReportingWaiter_NoticeSuppressed(t *testing.T) {
	reg, _ := withTestWiring(t)

	release := make(chan struct{})
	job := reg.Start(context.Background(), "asker", jobs.KindSubagent, "", func(ctx context.Context, jobID string) (string, bool, error) {
		<-release
		_, _, _ = reg.Ask(ctx, jobID, "which color?")
		return "done", false, nil
	})

	waitResult := make(chan *jobs.Job, 1)
	go func() {
		snap, ok := reg.Wait(context.Background(), job.ID, 5*time.Second)
		if !ok {
			t.Error("expected Wait to find the job")
			return
		}
		waitResult <- snap
	}()

	// Deterministic handshake (see waitUntilRegistered), not a sleep: only
	// ask once the Wait call has genuinely registered as a waiter — this
	// is what makes "parent is in wait" true at the moment Ask runs, not a
	// race against it.
	waitUntilRegistered(t, reg, job.ID)
	close(release)

	select {
	case snap := <-waitResult:
		if snap.Status != jobs.StatusWaitingAnswer {
			t.Fatalf("expected the Wait call to report StatusWaitingAnswer, got %s", snap.Status)
		}
		if snap.Question != "which color?" {
			t.Fatalf("expected the pending question on the Wait result, got %q", snap.Question)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return promptly after the job asked its question")
	}

	// The notice path must have been suppressed: give it a moment to have
	// fired if it were going to, then confirm nothing landed.
	time.Sleep(50 * time.Millisecond)
	if pending := JobNotices.Drain(); len(pending) != 0 {
		t.Fatalf("expected NO notice on the main queue when a Wait call was already blocked on the job, got %v", pending)
	}

	reg.Answer(job.ID, "blue", true)
}

// TestWiring_B7_ObserverOnly_NoticeNotSuppressed is C1's wiring-level
// regression coverage: a WaitObserve call blocked on the job — standing in
// for runWithHandoff's watcher, which never itself reports a question to
// anyone — must NOT suppress the onEvent notice. Contrast with
// TestWiring_B7_ReportingWaiter_NoticeSuppressed above: same shape, only
// the call (WaitObserve vs Wait) differs, and the outcome flips.
func TestWiring_B7_ObserverOnly_NoticeNotSuppressed(t *testing.T) {
	reg, _ := withTestWiring(t)

	release := make(chan struct{})
	job := reg.Start(context.Background(), "asker", jobs.KindSubagent, "", func(ctx context.Context, jobID string) (string, bool, error) {
		<-release
		_, _, _ = reg.Ask(ctx, jobID, "which color?")
		return "done", false, nil
	})

	go func() {
		reg.WaitObserve(context.Background(), job.ID, 5*time.Second)
	}()
	waitUntilObserverRegistered(t, reg, job.ID)
	close(release)

	deadline := time.Now().Add(3 * time.Second)
	var pending []string
	for time.Now().Before(deadline) {
		pending = JobNotices.Drain()
		if len(pending) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatalf("expected exactly one notice on the main queue (a WaitObserve call must not suppress it), got %v", pending)
	}

	reg.Answer(job.ID, "blue", true)
}

// TestWiring_B7_ParentNotInWait_NoticeDelivered is the other half: with
// nobody watching the job at all, the onEvent-driven notice is the only
// delivery, and it must actually arrive exactly once.
func TestWiring_B7_ParentNotInWait_NoticeDelivered(t *testing.T) {
	reg, _ := withTestWiring(t)

	job := reg.Start(context.Background(), "asker", jobs.KindSubagent, "", func(ctx context.Context, jobID string) (string, bool, error) {
		_, _, _ = reg.Ask(ctx, jobID, "which color?")
		return "done", false, nil
	})

	deadline := time.Now().Add(3 * time.Second)
	var pending []string
	for time.Now().Before(deadline) {
		pending = JobNotices.Drain()
		if len(pending) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatalf("expected exactly one notice on the main queue, got %v", pending)
	}

	reg.Answer(job.ID, "blue", true)
}
