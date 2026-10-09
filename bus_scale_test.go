package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/session"
)

// TestWiring_ManyNotices_NoneDropped publishes 200 notices to the
// orchestrator. One drain returns all of them, in order.
func TestWiring_ManyNotices_NoneDropped(t *testing.T) {
	withTestWiring(t)
	for i := range 200 {
		publishNotice(appBus, "", fmt.Sprintf("notice %03d", i), false)
	}

	got := drainNotices()
	if len(got) != 200 {
		t.Fatalf("drained %d notices, want 200", len(got))
	}
	for i, g := range got {
		if want := fmt.Sprintf("notice %03d", i); bodyOf(g) != want {
			t.Fatalf("notice %d = %q, want %q", i, bodyOf(g), want)
		}
	}
}

// TestWiring_StressChildrenAndAsk runs 20 children at the same time. Each
// child publishes 50 notices and one question. The orchestrator must drain
// exactly 1020 messages. The total stays below the 1024-message depth
// warning, so the warning adds no message. Run it with -race.
func TestWiring_StressChildrenAndAsk(t *testing.T) {
	withTestWiring(t)
	var wg sync.WaitGroup
	for child := range 20 {
		wg.Go(func() {
			for n := range 50 {
				publishNotice(appBus, "", fmt.Sprintf("child %02d notice %02d", child, n), false)
			}
			publishAsk(appBus, "", fmt.Sprintf("job-stress-%02d", child), 1, "[background job] stress question")
		})
	}
	wg.Wait()

	if got := drainNotices(); len(got) != 1020 {
		t.Fatalf("drained %d messages, want 1020", len(got))
	}
}

// TestWiring_ResumeOldID_FallsBackToOrchestrator sends a message to the ID of a
// job that has finished. The message is not lost. It reaches the orchestrator,
// and its text names the old agent.
func TestWiring_ResumeOldID_FallsBackToOrchestrator(t *testing.T) {
	reg, _ := withTestWiring(t)
	oldID, release := startLiveJob(t, reg)
	release()
	if _, ok := reg.Wait(context.Background(), oldID, 2*time.Second); !ok {
		t.Fatal("job never finished")
	}

	publishAgentMessage(appBus, oldID, orchestratorAddr, bus.OriginAgent, "after resume")

	got := drainNotices()
	if len(got) != 1 {
		t.Fatalf("orchestrator drained %d messages, want 1", len(got))
	}
	if !strings.Contains(got[0], oldID) || !strings.Contains(bodyOf(got[0]), "after resume") {
		t.Fatalf("notice = %q, want the old ID %s and the text", got[0], oldID)
	}
}

// TestBusJournalPath_OnlyWhenSessionDirExists checks that busJournalPath names
// the journal only after the session directory of the working directory exists.
func TestBusJournalPath_OnlyWhenSessionDirExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	if got := busJournalPath(); got != "" {
		t.Fatalf("busJournalPath() = %q before the session dir exists, want empty", got)
	}

	dir, err := session.SessionDir(wd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "bus.jsonl")
	if got := busJournalPath(); got != want {
		t.Fatalf("busJournalPath() = %q, want %q", got, want)
	}
}
