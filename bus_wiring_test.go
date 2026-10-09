package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/tools"
)

// TestAppBus_JournalHasNoToken publishes a message with a GitHub token and
// checks that the journal line keeps the token out of bus.jsonl.
func TestAppBus_JournalHasNoToken(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	path := filepath.Join(t.TempDir(), "bus.jsonl")
	b := newAppBus(path)

	if _, err := bus.Publish(b, bus.KindAgentMessage, bus.Addr{Type: bus.AddrOrchestrator},
		bus.Addr{Type: bus.AddrOrchestrator}, bus.OriginSystem, bus.AgentMessage{Text: "use " + token}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	b.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if strings.Contains(string(data), token) {
		t.Fatalf("journal holds the token in clear text: %s", data)
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("journal has no redaction marker: %s", data)
	}
}

// TestAppBus_NoJournalWithoutPath checks that an empty path leaves the bus
// in memory only, and that messages still reach subscribers.
func TestAppBus_NoJournalWithoutPath(t *testing.T) {
	b := newAppBus("")
	defer b.Close()
	sub := b.Subscribe("orch", bus.Filter{To: bus.Addr{Type: bus.AddrOrchestrator}})

	if _, err := bus.Publish(b, bus.KindAgentMessage, bus.Addr{Type: bus.AddrOrchestrator},
		bus.Addr{Type: bus.AddrOrchestrator}, bus.OriginSystem, bus.AgentMessage{Text: "hi"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if n := len(sub.Drain()); n != 1 {
		t.Fatalf("subscriber got %d messages, want 1", n)
	}
}

// TestBgBashNotice_DeliveredOnce_ToOrchestrator runs a real background command
// from the main conversation. Its completion notice reaches the orchestrator
// once: the first drain gets it, the second drain gets nothing.
func TestBgBashNotice_DeliveredOnce_ToOrchestrator(t *testing.T) {
	reg, _ := withTestWiring(t)
	_ = reg
	enableBackgroundBash(t)

	res := tools.RunTool(context.Background(), "bash", map[string]any{
		"command":           "echo once-only",
		"description":       "quick one",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("handoff failed: %s", res.Error)
	}
	id := bgJobID(t, res.Content)
	if _, ok := reg.Wait(context.Background(), id, 5*time.Second); !ok {
		t.Fatalf("job %q did not finish", id)
	}

	got := drainNotices()
	if len(got) != 1 || !strings.Contains(got[0], "quick one") {
		t.Fatalf("first drain = %q, want exactly one notice for the command", got)
	}
	if again := drainNotices(); len(again) != 0 {
		t.Fatalf("second drain = %q, want nothing", again)
	}
}

// startLiveJob starts a job on the test registry that stays running until
// the returned function is called. The job's inbox is open once Start returns.
func startLiveJob(t *testing.T, reg *jobs.Registry) (string, func()) {
	t.Helper()
	release := make(chan struct{})
	job := reg.Start(context.Background(), "agent", jobs.KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "", false, nil
	})
	return job.ID, func() { close(release) }
}

// TestBgBashNotice_DeliveredOnce_ToAgentInbox sends a notice to a live agent.
// The agent's inbox gets it once, and the orchestrator gets nothing.
func TestBgBashNotice_DeliveredOnce_ToAgentInbox(t *testing.T) {
	reg, _ := withTestWiring(t)
	id, release := startLiveJob(t, reg)
	defer release()

	publishNotice(appBus, id, "[background command] done", false)

	got := agentInboxes.drain(id)
	if len(got) != 1 || bodyOf(got[0]) != "[background command] done" {
		t.Fatalf("inbox drain = %q, want the one notice", got)
	}
	if again := agentInboxes.drain(id); len(again) != 0 {
		t.Fatalf("second inbox drain = %q, want nothing", again)
	}
	if orch := drainNotices(); len(orch) != 0 {
		t.Fatalf("orchestrator got %q, want nothing", orch)
	}
}

// TestBgBashNotice_ToFinishedAgent_GoesToOrchestratorTagged checks that a
// notice for an agent that is no longer live reaches the orchestrator once,
// tagged with the agent it was meant for.
func TestBgBashNotice_ToFinishedAgent_GoesToOrchestratorTagged(t *testing.T) {
	withTestWiring(t)

	publishNotice(appBus, "job-gone", "[background command] late", false)

	got := drainNotices()
	want := "[for agent job-gone, which has already finished — forwarded here instead] [background command] late"
	if len(got) != 1 || bodyOf(got[0]) != want {
		t.Fatalf("orchestrator drain = %q, want one tagged notice", got)
	}
	if again := drainNotices(); len(again) != 0 {
		t.Fatalf("second drain = %q, want nothing", again)
	}
}

// TestInbox_UndeliveredAtFinish_NothingLost checks that a notice still waiting
// in the inbox when the agent finishes goes to the orchestrator, once.
func TestInbox_UndeliveredAtFinish_NothingLost(t *testing.T) {
	reg, _ := withTestWiring(t)
	id, release := startLiveJob(t, reg)
	publishNotice(appBus, id, "[background command] waiting", false)

	release()
	if _, ok := reg.Wait(context.Background(), id, 5*time.Second); !ok {
		t.Fatalf("job %q did not finish", id)
	}

	// The terminal event, which forwards the inbox, runs after Wait returns.
	var got []string
	deadline := time.Now().Add(5 * time.Second)
	for len(got) == 0 && time.Now().Before(deadline) {
		got = drainNotices()
		time.Sleep(5 * time.Millisecond)
	}
	if len(got) != 1 || !strings.Contains(got[0], "waiting") || !strings.Contains(got[0], id) {
		t.Fatalf("orchestrator drain = %q, want the forwarded notice", got)
	}
	if again := drainNotices(); len(again) != 0 {
		t.Fatalf("second drain = %q, want nothing", again)
	}
}

// TestInbox_OpenTwice_OneSubscription checks that one agent never gets two
// inboxes, so a notice is not delivered twice.
func TestInbox_OpenTwice_OneSubscription(t *testing.T) {
	reg, _ := withTestWiring(t)
	id, release := startLiveJob(t, reg)
	defer release()
	agentInboxes.open(id)
	agentInboxes.open(id)

	publishNotice(appBus, id, "once", false)

	if got := agentInboxes.drain(id); len(got) != 1 {
		t.Fatalf("inbox drain = %q, want exactly one notice", got)
	}
}
