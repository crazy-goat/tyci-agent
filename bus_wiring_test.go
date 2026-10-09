package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/bus"
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
