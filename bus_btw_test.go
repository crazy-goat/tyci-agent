package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestBtwAnswer_ToOrchestrator_DeliveredOnce publishes a btw answer with no
// parent. The orchestrator gets it once, in the usual evaluation text.
func TestBtwAnswer_ToOrchestrator_DeliveredOnce(t *testing.T) {
	withTestWiring(t)
	publishBtwAnswer(appBus, "", "job-btw-1", "is it safe?", "yes")

	got := drainNotices()
	if len(got) != 1 || bodyOf(got[0]) != btwEvaluationNotice("is it safe?", "job-btw-1", "yes") {
		t.Fatalf("orchestrator drain = %q, want the evaluation notice once", got)
	}
	if again := drainNotices(); len(again) != 0 {
		t.Fatalf("second drain = %q, want nothing", again)
	}
}

// TestBtwAnswer_ToLiveParent_ReachesItsInboxOnly publishes a btw answer to a
// live parent. The parent's inbox gets it, and the orchestrator gets nothing.
func TestBtwAnswer_ToLiveParent_ReachesItsInboxOnly(t *testing.T) {
	reg, _ := withTestWiring(t)
	parent, release := startLiveJob(t, reg)
	defer release()

	publishBtwAnswer(appBus, parent, "job-btw-2", "is it fast?", "yes")

	got := agentInboxes.drain(parent)
	if len(got) != 1 || !strings.Contains(got[0], "is it fast?") {
		t.Fatalf("parent inbox = %q, want the evaluation notice once", got)
	}
	if other := drainNotices(); len(other) != 0 {
		t.Fatalf("orchestrator drain = %q, want nothing", other)
	}
}

// TestBtwAnswer_PublishErrorIsLoggedNotFatal publishes a btw answer on a closed
// bus. The call must not panic, and the error must be logged.
func TestBtwAnswer_PublishErrorIsLoggedNotFatal(t *testing.T) {
	withTestWiring(t)
	var log bytes.Buffer
	orig := busLog
	busLog = &log
	t.Cleanup(func() { busLog = orig })

	closed := newAppBus("")
	closed.Close()
	publishBtwAnswer(closed, "", "job-btw-3", "q", "a")

	if !strings.Contains(log.String(), "not published") {
		t.Fatalf("log = %q, want the publish error", log.String())
	}
}
