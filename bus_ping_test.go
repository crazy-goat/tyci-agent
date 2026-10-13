package main

import (
	"os"
	"path/filepath"
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
	if len(got) != 1 || !strings.Contains(got[0], "[watchdog] Agent job-17 () has shown no activity for 4m.") {
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

// TestPingMissed_TextMatchesOldAlarm checks that the formatted alarm equals the
// text the watchdog sent before #194, byte for byte, with and without a
// progress note.
func TestPingMissed_TextMatchesOldAlarm(t *testing.T) {
	withTestWiring(t)
	cases := []struct {
		name string
		a    watchdog.Alarm
		want string
	}{
		{
			name: "with note",
			a: watchdog.Alarm{Agent: "job-17", Description: "build the API",
				LastNote: "parsing routes", QuietFor: 4*time.Minute + 20*time.Second},
			want: "[watchdog] Agent job-17 (build the API) has shown no activity for 4m.\n" +
				"Last progress note: \"parsing routes\".\n" +
				"You may: send it a message (message), or cancel it (kill_job).",
		},
		{
			name: "without note",
			a: watchdog.Alarm{Agent: "job-18", Description: "lint",
				QuietFor: 20 * time.Second},
			want: "[watchdog] Agent job-18 (lint) has shown no activity for 20s.\n" +
				"Last progress note: \"none\".\n" +
				"You may: send it a message (message), or cancel it (kill_job).",
		},
	}
	for _, c := range cases {
		publishPingMissed(appBus, "", c.a)
		got := drainNotices()
		if len(got) != 1 || bodyOf(got[0]) != c.want {
			t.Fatalf("%s: text = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestPingMissed_OnePublishPerMiss checks that one alarm is one bus message.
// The journal gets one ping.missed line, the Bus tab one event, the parent one
// copy and the TUI one copy.
func TestPingMissed_OnePublishPerMiss(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "bus.jsonl")
	b := bus.New(bus.WithJournal(journal))
	defer b.Close()
	parent := b.Subscribe("test-parent", bus.Filter{To: agentAddr("p"), Kinds: []bus.Kind{bus.KindPingMissed}})
	tui := subscribePings(b)

	publishPingMissed(b, "p", watchdog.Alarm{Agent: "job-3", QuietFor: time.Minute})

	if n := len(parent.Drain()); n != 1 {
		t.Fatalf("parent got %d ping.missed, want 1", n)
	}
	if n := len(tui.Drain()); n != 1 {
		t.Fatalf("tui got %d ping.missed, want 1", n)
	}
	seqs := map[uint64]bool{}
	for _, ev := range b.Events() {
		if ev.Msg.Kind == bus.KindPingMissed {
			seqs[ev.Msg.Seq] = true
		}
	}
	if len(seqs) != 1 {
		t.Fatalf("bus events for %d messages, want 1", len(seqs))
	}
	b.Close()
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), `"kind":"ping.missed"`); n != 1 {
		t.Fatalf("journal has %d ping.missed lines, want 1:\n%s", n, data)
	}
}

// TestWatchdogRecovered_PublishesOnlyWithTUISubscription checks that a recovery
// writes no ping.ok when no TUI subscribed, as in a headless run. With the
// subscription, it publishes one ping.ok that the TUI gets.
func TestWatchdogRecovered_PublishesOnlyWithTUISubscription(t *testing.T) {
	withTestWiring(t)
	pingOKWanted.Store(false)
	t.Cleanup(func() { pingOKWanted.Store(false) })

	headless := filepath.Join(t.TempDir(), "headless.jsonl")
	hb := bus.New(bus.WithJournal(headless))
	appBus = hb
	watchdogRecovered("job-1")
	if n := len(hb.Events()); n != 0 {
		t.Fatalf("headless bus has %d events, want none", n)
	}
	hb.Close()
	if data, err := os.ReadFile(headless); err == nil && strings.Contains(string(data), `"kind":"ping.ok"`) {
		t.Fatalf("headless journal has a ping.ok line:\n%s", data)
	}

	tuiJournal := filepath.Join(t.TempDir(), "tui.jsonl")
	tb := bus.New(bus.WithJournal(tuiJournal))
	defer tb.Close()
	appBus = tb
	tui := subscribePings(tb)
	watchdogRecovered("job-1")
	msgs := tui.Drain()
	if len(msgs) != 1 || msgs[0].Kind != bus.KindPingOK {
		t.Fatalf("tui got %d messages, want one ping.ok", len(msgs))
	}
	tb.Close()
	data, err := os.ReadFile(tuiJournal)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), `"kind":"ping.ok"`); n != 1 {
		t.Fatalf("tui journal has %d ping.ok lines, want 1:\n%s", n, data)
	}
}
