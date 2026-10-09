package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestAsk_ToOrchestrator_DeliveredOnce publishes a question with no parent.
// The orchestrator gets it once.
func TestAsk_ToOrchestrator_DeliveredOnce(t *testing.T) {
	withTestWiring(t)
	publishAsk(appBus, "", "job-ask-1", 1, "[background job] Q1")

	if got := drainNotices(); len(got) != 1 || bodyOf(got[0]) != "[background job] Q1" {
		t.Fatalf("orchestrator drain = %q, want the question once", got)
	}
	if again := drainNotices(); len(again) != 0 {
		t.Fatalf("second drain = %q, want nothing", again)
	}
}

// TestAsk_ToLiveParent_ReachesItsInboxOnly publishes a question to a live
// parent. The parent's inbox gets it, and the orchestrator gets nothing.
func TestAsk_ToLiveParent_ReachesItsInboxOnly(t *testing.T) {
	reg, _ := withTestWiring(t)
	parent, release := startLiveJob(t, reg)
	defer release()

	publishAsk(appBus, parent, "job-child", 1, "[background job] Q2")

	if got := agentInboxes.drain(parent); len(got) != 1 || bodyOf(got[0]) != "[background job] Q2" {
		t.Fatalf("parent inbox = %q, want the question once", got)
	}
	if got := drainNotices(); len(got) != 0 {
		t.Fatalf("orchestrator drain = %q, want nothing", got)
	}
}

// TestAskDedup_MarkBeforePublish marks a question before it is published. The
// question is then left out of the drain.
func TestAskDedup_MarkBeforePublish(t *testing.T) {
	withTestWiring(t)
	markAskShown("job-dedup-1", 1)
	publishAsk(appBus, "", "job-dedup-1", 1, "[background job] Q3")

	if got := drainNotices(); len(got) != 0 {
		t.Fatalf("drain = %q, want the marked question left out", got)
	}
}

// TestAskDedup_PublishBeforeMark publishes a question and marks it while it
// still waits in the queue. The drain still leaves it out.
func TestAskDedup_PublishBeforeMark(t *testing.T) {
	withTestWiring(t)
	publishAsk(appBus, "", "job-dedup-2", 1, "[background job] Q4")
	markAskShown("job-dedup-2", 1)

	if got := drainNotices(); len(got) != 0 {
		t.Fatalf("drain = %q, want the marked question left out", got)
	}
}

// TestAskDedup_OtherSeqWithSameText keeps a later question with the same text
// when only the first one was marked. The key is the seq, not the text.
func TestAskDedup_OtherSeqWithSameText(t *testing.T) {
	withTestWiring(t)
	markAskShown("job-dedup-3", 1)
	publishAsk(appBus, "", "job-dedup-3", 1, "[background job] same text")
	publishAsk(appBus, "", "job-dedup-3", 2, "[background job] same text")

	got := drainNotices()
	if len(got) != 1 || bodyOf(got[0]) != "[background job] same text" {
		t.Fatalf("drain = %q, want only the second question", got)
	}
}

// TestAskDedup_MidLevelInbox marks a question that a handoff carried. The
// middle agent's inbox does not show it again, and a later question still
// arrives.
func TestAskDedup_MidLevelInbox(t *testing.T) {
	reg, _ := withTestWiring(t)
	parent, release := startLiveJob(t, reg)
	defer release()

	publishAsk(appBus, parent, "job-deep", 1, "[background job] first")
	markAskShown("job-deep", 1)
	publishAsk(appBus, parent, "job-deep", 2, "[background job] second")

	got := agentInboxes.drain(parent)
	if len(got) != 1 || bodyOf(got[0]) != "[background job] second" {
		t.Fatalf("parent inbox = %q, want only the second question", got)
	}
}

// TestAsk_PublishErrorIsLoggedNotFatal publishes a question on a closed bus.
// The call must not panic, and the error must be logged.
func TestAsk_PublishErrorIsLoggedNotFatal(t *testing.T) {
	withTestWiring(t)
	var log bytes.Buffer
	orig := busLog
	busLog = &log
	t.Cleanup(func() { busLog = orig })

	closed := newAppBus("")
	closed.Close()
	publishAsk(closed, "", "job-closed", 1, "[background job] Q5")

	if !strings.Contains(log.String(), "not published") {
		t.Fatalf("log = %q, want the publish error", log.String())
	}
}

// TestAsk_ToFinishedParent_GoesToOrchestratorTagged publishes a question to a
// parent that is not live. The orchestrator gets it, with the tag that names
// the parent.
func TestAsk_ToFinishedParent_GoesToOrchestratorTagged(t *testing.T) {
	withTestWiring(t)
	publishAsk(appBus, "job-finished", "job-child", 1, "[background job] Q6")

	got := drainNotices()
	if len(got) != 1 || !strings.Contains(got[0], "job-finished") || !strings.HasSuffix(got[0], "[background job] Q6") {
		t.Fatalf("orchestrator drain = %q, want the question tagged with job-finished", got)
	}
}
