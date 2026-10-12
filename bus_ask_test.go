package main

import (
	"strings"
	"testing"
)

// TestAsk_ToOrchestrator_DeliveredOnce publishes a question with no parent.
// The orchestrator gets it once.
func TestAsk_ToOrchestrator_DeliveredOnce(t *testing.T) {
	withTestWiring(t)
	if _, err := publishAsk(appBus, "", "job-ask-1", "Q1"); err != nil {
		t.Fatalf("publishAsk: %v", err)
	}

	got := drainNotices()
	if len(got) != 1 || !strings.Contains(bodyOf(got[0]), `asks: "Q1"`) {
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

	if _, err := publishAsk(appBus, parent, "job-child", "Q2"); err != nil {
		t.Fatalf("publishAsk: %v", err)
	}

	if got := agentInboxes.drain(parent); len(got) != 1 || !strings.Contains(bodyOf(got[0]), `asks: "Q2"`) {
		t.Fatalf("parent inbox = %q, want the question once", got)
	}
	if got := drainNotices(); len(got) != 0 {
		t.Fatalf("orchestrator drain = %q, want nothing", got)
	}
}

// TestAsk_PublishErrorIsReturned publishes a question on a closed bus. The
// call must not panic, and it must return the error.
func TestAsk_PublishErrorIsReturned(t *testing.T) {
	withTestWiring(t)

	closed := newAppBus("")
	closed.Close()
	if _, err := publishAsk(closed, "", "job-closed", "Q5"); err == nil {
		t.Fatal("publishAsk on a closed bus returned no error")
	}
}

// TestAsk_ToFinishedParent_GoesToOrchestratorTagged publishes a question to a
// parent that is not live. The orchestrator gets it, with the tag that names
// the parent.
func TestAsk_ToFinishedParent_GoesToOrchestratorTagged(t *testing.T) {
	withTestWiring(t)
	if _, err := publishAsk(appBus, "job-finished", "job-child", "Q6"); err != nil {
		t.Fatalf("publishAsk: %v", err)
	}

	got := drainNotices()
	if len(got) != 1 || !strings.Contains(got[0], "job-finished") || !strings.Contains(got[0], `asks: "Q6"`) {
		t.Fatalf("orchestrator drain = %q, want the question tagged with job-finished", got)
	}
}

// TestAsk_PublishErrorIsLoggedNotFatal publishes a question on a closed bus.
// The call must not panic, and the error must be returned to the caller,
// which logs it (see jobAskerAdapter.Ask in btw.go).
func TestAsk_PublishErrorIsLoggedNotFatal(t *testing.T) {
	withTestWiring(t)

	closed := newAppBus("")
	closed.Close()
	if _, err := publishAsk(closed, "", "job-closed", "[background job] Q5"); err == nil {
		t.Fatal("expected publishAsk to fail on a closed bus")
	}
}
