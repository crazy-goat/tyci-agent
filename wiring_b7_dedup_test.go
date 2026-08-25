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
// These tests drive the exact production wiring (wireTools, via
// withTestWiring) rather than reimplementing the dedup decision.

import (
	"context"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// TestWiring_B7_ParentInWait_NoticeSuppressed is the "parent is in wait"
// case: a caller already blocked in JobRegistry.Wait for this exact job
// when it asks a question must get exactly one delivery — its own Wait
// call's Waiting result — and the main notice queue must stay empty.
func TestWiring_B7_ParentInWait_NoticeSuppressed(t *testing.T) {
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

	// Give the goroutine above time to actually register as a waiter
	// (jobs.Job.waiters, incremented under Registry.mu at the top of Wait)
	// before the question is asked — this is what makes "parent is in
	// wait" true at the moment Ask runs, not a race against it.
	time.Sleep(30 * time.Millisecond)
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

// TestWiring_B7_ParentNotInWait_NoticeDelivered is the other half: with
// nobody blocked in Wait for the job, the onEvent-driven notice is the
// only delivery, and it must actually arrive exactly once.
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
