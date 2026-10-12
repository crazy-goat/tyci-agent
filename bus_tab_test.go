package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/bus"
)

func TestBusTabRows_RedactsSecrets(t *testing.T) {
	const token = "ghp_0123456789abcdefghijklmnopqrstuvwxyz"
	payload, err := json.Marshal(map[string]string{"text": "use " + token + " for the push"})
	if err != nil {
		t.Fatal(err)
	}
	events := []bus.Event{{
		Msg: bus.Message{
			Seq:     1,
			Kind:    bus.KindAgentMessage,
			From:    bus.Addr{Type: bus.AddrAgent, ID: "a1"},
			To:      bus.Addr{Type: bus.AddrOrchestrator},
			Payload: payload,
		},
		State: bus.Sent,
	}}
	rows := busTabRows(events)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if strings.Contains(rows[0].Text, token) {
		t.Fatalf("row text leaks the token: %q", rows[0].Text)
	}
	if !strings.Contains(rows[0].Text, "[REDACTED]") {
		t.Fatalf("row text %q has no mask", rows[0].Text)
	}
}

func TestBusTabRows_OneRowPerMessageNewestFirst(t *testing.T) {
	msg := func(seq uint64, from, to bus.Addr, text string) bus.Message {
		payload, _ := json.Marshal(map[string]string{"text": text})
		return bus.Message{Seq: seq, Kind: bus.KindAgentMessage, From: from, To: to, Payload: payload}
	}
	human := bus.Addr{Type: bus.AddrUser}
	orch := bus.Addr{Type: bus.AddrOrchestrator}
	events := []bus.Event{
		{Msg: msg(1, human, orch, "first"), State: bus.Sent},
		{Msg: msg(2, orch, bus.Addr{Type: bus.AddrSubtree}, "second"), State: bus.Sent},
		{Msg: msg(1, human, orch, "first"), State: bus.Received},
		{Msg: bus.Message{Kind: "job.status", From: orch, To: bus.Addr{Type: bus.AddrAgent, ID: "a9"}}, State: bus.Failed},
	}
	rows := busTabRows(events)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(rows), rows)
	}
	// The failed event has no Seq and no text, so it shows its kind.
	if rows[0].State != "failed" || rows[0].Text != "job.status" || rows[0].To != "a9" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	// Message 1 moved to its newest event, Received, before message 2.
	if rows[1].Seq != 1 || rows[1].State != "received" || rows[1].From != "human" || rows[1].To != "orchestrator" {
		t.Errorf("row 1 = %+v", rows[1])
	}
	if rows[2].Seq != 2 || rows[2].State != "sent" || rows[2].To != "*" {
		t.Errorf("row 2 = %+v", rows[2])
	}
}
