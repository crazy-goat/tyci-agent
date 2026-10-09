package main

import (
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/internal/watchdog"
)

// TestPingMissed_ToOrchestrator_DeliveredOnce sends a watchdog alarm with no
// live parent. The orchestrator gets it once, with kind ping.missed.
func TestPingMissed_ToOrchestrator_DeliveredOnce(t *testing.T) {
	withTestWiring(t)
	sub := appBus.Subscribe("test-ping", bus.Filter{To: orchestratorAddr, Kinds: []bus.Kind{bus.KindPingMissed}})
	defer sub.Close()

	if !watchdogNotify("", watchdog.Alarm{Agent: "job-17", QuietFor: 4*time.Minute + 20*time.Second}) {
		t.Fatal("watchdogNotify to the orchestrator returned false")
	}

	msgs := sub.Drain()
	if len(msgs) != 1 || msgs[0].Kind != bus.KindPingMissed {
		t.Fatalf("ping.missed messages = %d, want one", len(msgs))
	}
	got := drainNotices()
	if len(got) != 1 || !strings.Contains(got[0], "[watchdog] Agent job-17 has shown no activity for 4m.") {
		t.Fatalf("orchestrator drain = %q, want the alarm once", got)
	}
	if again := drainNotices(); len(again) != 0 {
		t.Fatalf("second drain = %q, want nothing", again)
	}
}

// TestPingMissed_ToLiveParent_ReachesItsInboxOnly sends an alarm to a live
// parent. The parent's inbox gets it, and the orchestrator gets nothing.
func TestPingMissed_ToLiveParent_ReachesItsInboxOnly(t *testing.T) {
	reg, _ := withTestWiring(t)
	parent, release := startLiveJob(t, reg)
	defer release()

	if !watchdogNotify(parent, watchdog.Alarm{Agent: "job-child", QuietFor: 3 * time.Minute}) {
		t.Fatal("watchdogNotify to a live parent returned false")
	}
	if got := agentInboxes.drain(parent); len(got) != 1 || !strings.Contains(got[0], "job-child") {
		t.Fatalf("parent inbox = %q, want the alarm once", got)
	}
	if got := drainNotices(); len(got) != 0 {
		t.Fatalf("orchestrator drain = %q, want nothing", got)
	}
}

// TestPingMissed_ToFinishedParent_ClimbsWhenNotLive checks that an alarm to a
// parent that is not live is refused, so that the watchdog climbs.
func TestPingMissed_ToFinishedParent_ClimbsWhenNotLive(t *testing.T) {
	withTestWiring(t)
	if watchdogNotify("job-finished", watchdog.Alarm{Agent: "job-child", QuietFor: time.Minute}) {
		t.Fatal("watchdogNotify to a finished parent returned true, want false")
	}
	if got := drainNotices(); len(got) != 0 {
		t.Fatalf("orchestrator drain = %q, want nothing", got)
	}
}

// TestFormatIdle checks the rounding of the quiet time in an alarm.
func TestFormatIdle(t *testing.T) {
	for in, want := range map[time.Duration]string{
		20 * time.Second:               "20s",
		4*time.Minute + 50*time.Second: "5m",
		4*time.Minute + 20*time.Second: "4m",
	} {
		if got := formatIdle(in); got != want {
			t.Errorf("%v: got %q want %q", in, got, want)
		}
	}
}
