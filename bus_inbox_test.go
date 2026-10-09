package main

import (
	"testing"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/jobs"
)

// TestInboxEvent_LateRunningDoesNotReopen delivers the start, terminal and a
// late Running snapshot in that order, as the hook can after #131. The late
// snapshot must not reopen the inbox, so no subscription is left behind.
func TestInboxEvent_LateRunningDoesNotReopen(t *testing.T) {
	inboxes := newInboxSet(bus.New())
	start := jobs.Job{ID: "job-late", Status: jobs.StatusRunning, EventSeq: 1}
	done := jobs.Job{ID: "job-late", Status: jobs.StatusDone, EventSeq: 3}
	late := jobs.Job{ID: "job-late", Status: jobs.StatusRunning, EventSeq: 2}

	inboxEvent(inboxes, start)
	if _, ok := inboxes.subs["job-late"]; !ok {
		t.Fatal("start snapshot did not open the inbox")
	}
	inboxEvent(inboxes, done)
	inboxEvent(inboxes, late)
	if n := len(inboxes.subs); n != 0 {
		t.Fatalf("open inboxes = %d after a late Running snapshot, want 0", n)
	}
}

// TestInboxEvent_WaitingAnswerKeepsInbox checks that a snapshot that waits
// for an answer neither opens nor closes the inbox.
func TestInboxEvent_WaitingAnswerKeepsInbox(t *testing.T) {
	inboxes := newInboxSet(bus.New())
	inboxEvent(inboxes, jobs.Job{ID: "job-w", Status: jobs.StatusRunning, EventSeq: 1})
	inboxEvent(inboxes, jobs.Job{ID: "job-w", Status: jobs.StatusWaitingAnswer, EventSeq: 2})
	if _, ok := inboxes.subs["job-w"]; !ok {
		t.Fatal("waiting snapshot closed the inbox")
	}
}
