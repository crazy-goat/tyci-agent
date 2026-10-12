package main

// M-1: a message posted to a running async subagent's mailbox is delivered
// at that job's own agent loop's next iteration boundary — the same
// mechanism agent.Config.NextMessages already provides for the main agent,
// wired per-job via tools.JobMailboxNextMessages (see agentRunner.run in
// main.go). Mirrors TestWiring_Q1_AskAnswerRoundTrip's synchronization
// trick: turn 0 blocks inside "ask_parent" so the test can deterministically
// post to the job's mailbox and confirm delivery through the REAL
// production wiring, before releasing it.

import (
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
)

// countUserMessages counts user-role messages in req.Messages — the initial
// task plus one per drained mailbox message.
func countUserMessages(req connector.Request) int {
	n := 0
	for _, m := range req.Messages {
		if m.Role == "user" {
			n++
		}
	}
	return n
}

// containsUserText reports whether any user-role message in req carries
// text exactly matching want.
func containsUserText(req connector.Request, want string) bool {
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		for _, c := range m.Content {
			// A message from an agent carries the sender tag, see tagSender.
			if c.Text == want || bodyOf(c.Text) == want {
				return true
			}
		}
	}
	return false
}

// TestWiring_M2_PostToUnknownJobFails guards the negative path through the
// real wiring: posting to a job id nothing in the registry recognizes
// (never existed, or already pruned) must fail rather than silently
// succeed — a caller (the "message" tool, or "/msg") needs to know its
// message was not queued anywhere.
func TestWiring_M2_PostToUnknownJobFails(t *testing.T) {
	reg, _ := withTestWiring(t)
	if (jobMailboxAdapter{reg: reg}).Post("job-does-not-exist-1", "hi") {
		t.Fatal("expected Post to fail for an unknown job id")
	}
	if _, ok := reg.Resolve("job-does-not-exist-1"); ok {
		t.Fatal("expected Resolve to fail for an unknown job id")
	}
}
