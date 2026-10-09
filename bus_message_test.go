package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/bus"
)

// TestAgentMessage_ToLiveJob_DeliveredOnce posts a message to a live job. The
// job's inbox gets it once, and the orchestrator gets nothing.
func TestAgentMessage_ToLiveJob_DeliveredOnce(t *testing.T) {
	reg, _ := withTestWiring(t)
	id, release := startLiveJob(t, reg)
	defer release()

	if !(jobMailboxAdapter{reg: reg}).Post(id, "steer left") {
		t.Fatal("Post to a live job returned false")
	}

	if got := (jobMailboxAdapter{reg: reg}).Drain(id); len(got) != 1 || bodyOf(got[0]) != "steer left" {
		t.Fatalf("drain = %q, want the message once", got)
	}
	if again := (jobMailboxAdapter{reg: reg}).Drain(id); len(again) != 0 {
		t.Fatalf("second drain = %q, want nothing", again)
	}
	if got := drainNotices(); len(got) != 0 {
		t.Fatalf("orchestrator drain = %q, want nothing", got)
	}
}

// TestAgentMessage_ToFinishedJob_Refused posts to a job that is not live. Post
// reports false, and nothing is published for it.
func TestAgentMessage_ToFinishedJob_Refused(t *testing.T) {
	reg, _ := withTestWiring(t)
	if (jobMailboxAdapter{reg: reg}).Post("job-not-started", "too late") {
		t.Fatal("Post to an unknown job returned true")
	}
	if got := drainNotices(); len(got) != 0 {
		t.Fatalf("orchestrator drain = %q, want nothing", got)
	}
}

// TestAgentMessage_PublishErrorIsLoggedNotFatal publishes a message on a closed
// bus. The call must not panic, and the error must be logged.
func TestAgentMessage_PublishErrorIsLoggedNotFatal(t *testing.T) {
	withTestWiring(t)
	var log bytes.Buffer
	orig := busLog
	busLog = &log
	t.Cleanup(func() { busLog = orig })

	closed := newAppBus("")
	closed.Close()
	publishAgentMessage(closed, "job-closed", orchestratorAddr, bus.OriginAgent, "hi")

	if !strings.Contains(log.String(), "not published") {
		t.Fatalf("log = %q, want the publish error", log.String())
	}
}
