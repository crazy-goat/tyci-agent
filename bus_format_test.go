package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/agent"
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

	if err := (jobMailboxAdapter{reg: reg}).Post("", child, "steer this way"); err != nil {
		t.Fatalf("Post to a live child failed: %v", err)
	}
	got := agentInboxes.drain(child)
	if len(got) != 1 {
		t.Fatalf("inbox drain = %q, want one message", got)
	}
	if got[0] == "steer this way" || !strings.HasPrefix(got[0], "[notice from=") || !strings.Contains(got[0], "kind=agent.message") {
		t.Fatalf("inbox line = %q, want the sender tag", got[0])
	}
}

// TestNoticeFormatUsesSharedPrefix: the notice tag must start with the prefix
// the agent loop checks, or a notice is taken for a person's line.
func TestNoticeFormatUsesSharedPrefix(t *testing.T) {
	m := rawMessage(t, bus.KindNoticeCompletion, bus.Addr{Type: bus.AddrOrchestrator}, bus.OriginSystem, bus.Completion{Text: "done"})
	got := formatNotices([]bus.Message{m})
	if len(got) != 1 || !strings.HasPrefix(got[0], agent.NoticePrefix) {
		t.Fatalf("formatNotices = %q, want a line starting with agent.NoticePrefix", got)
	}
}

// TestNoticeLinesPassIsNoticeLine checks that every line that formatNotices
// writes for a harness notice is recognised by agent.IsNoticeLine, also when
// the bus rerouted the message. A line that is not recognised is taken for a
// person's line, and the interrupt note comes back.
func TestNoticeLinesPassIsNoticeLine(t *testing.T) {
	reply := rawMessage(t, bus.KindAskReply, bus.Addr{Type: bus.AddrOrchestrator}, bus.OriginAgent, bus.AskReply{Question: "q7", Answer: "use main"})
	completion := rawMessage(t, bus.KindNoticeCompletion, bus.Addr{Type: bus.AddrOrchestrator}, bus.OriginSystem, bus.Completion{Text: "done"})
	orig := bus.Addr{Type: bus.AddrAgent, ID: "job-1"}
	rerouted := []bus.Message{reply, completion}
	for i := range rerouted {
		rerouted[i].OrigTo = &orig
	}
	cases := map[string]bus.Message{
		"ask reply":           reply,
		"ask reply rerouted":  rerouted[0],
		"completion":          completion,
		"completion rerouted": rerouted[1],
	}
	for name, m := range cases {
		got := formatNotices([]bus.Message{m})
		if len(got) != 1 || !agent.IsNoticeLine(got[0]) {
			t.Errorf("%s: formatNotices = %q, want a line that agent.IsNoticeLine accepts", name, got)
		}
	}
}

// TestAskReplyReroutedNoticeText checks the text of a rerouted ask reply. The
// reroute note is inside the reply tag, so the line starts with the tag.
func TestAskReplyReroutedNoticeText(t *testing.T) {
	m := rawMessage(t, bus.KindAskReply, bus.Addr{Type: bus.AddrOrchestrator}, bus.OriginAgent, bus.AskReply{Question: "q7", Answer: "use main"})
	m.OrigTo = &bus.Addr{Type: bus.AddrAgent, ID: "job-1"}
	got := formatNotices([]bus.Message{m})
	want := `<tyci-notice kind=ask.reply id=q7 from=agent>[for agent job-1, which has already finished — forwarded here instead] use main</tyci-notice>`
	if len(got) != 1 || got[0] != want {
		t.Fatalf("formatNotices = %q, want [%q]", got, want)
	}
}
