package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
)

// bodyOf strips the sender tag that formatNotices puts in front of a notice.
func bodyOf(s string) string {
	if !strings.HasPrefix(s, "[notice from=") {
		return s
	}
	i := strings.Index(s, "] ")
	if i < 0 {
		return s
	}
	return s[i+2:]
}

// rawMessage builds a bus message as the inbox would hand it over.
func rawMessage(t *testing.T, kind bus.Kind, from bus.Addr, origin bus.Origin, payload any) bus.Message {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return bus.Message{Seq: 1, At: time.Now(), Kind: kind, From: from, Origin: origin, Payload: raw}
}

// TestFormatInbox_NonHumanOriginIsTagged checks that every origin except a
// person gets the sender tag. Only the human origin is a plain user turn.
func TestFormatInbox_NonHumanOriginIsTagged(t *testing.T) {
	msgs := []bus.Message{
		rawMessage(t, bus.KindAgentMessage, bus.Addr{Type: bus.AddrAgent, ID: "job-17"}, bus.OriginAgent, bus.AgentMessage{Text: "from agent"}),
		rawMessage(t, bus.KindNoticeCompletion, bus.Addr{Type: bus.AddrOrchestrator}, bus.OriginSystem, bus.Completion{Text: "from system"}),
		rawMessage(t, bus.KindAgentMessage, bus.Addr{Type: bus.AddrUser}, bus.OriginHuman, bus.AgentMessage{Text: "from person"}),
	}
	got := formatNotices(msgs)
	want := []string{
		"[notice from=agent:job-17 kind=agent.message] from agent",
		"[notice from=orchestrator kind=notice.completion] from system",
		"from person",
	}
	if len(got) != len(want) {
		t.Fatalf("formatNotices = %q, want %d lines", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestFormatInbox_ChildCannotSendPlainUserTurn checks the path a child uses to
// send a message. The message tool posts with the agent origin, so the child's
// inbox shows the text with the sender tag, never as a plain user turn.
func TestFormatInbox_ChildCannotSendPlainUserTurn(t *testing.T) {
	reg, _ := withTestWiring(t)
	child, release := startLiveJob(t, reg)
	defer release()

	if !(jobMailboxAdapter{reg: reg}).Post(child, "steer this way") {
		t.Fatal("Post to a live child failed")
	}
	got := agentInboxes.drain(child)
	if len(got) != 1 {
		t.Fatalf("inbox drain = %q, want one message", got)
	}
	if got[0] == "steer this way" || !strings.HasPrefix(got[0], "[notice from=") || !strings.Contains(got[0], "kind=agent.message") {
		t.Fatalf("inbox line = %q, want the sender tag", got[0])
	}
}
