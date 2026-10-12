package main

// Ask flow through the real wiring (withTestWiring, wireTools via
// jobAskerAdapter and jobAnswererAdapter). ask_parent publishes an
// ask.request and returns at once. answer_job sends an ask.reply to the child
// that asked. There is no dedupe: each question is published once.

import (
	"context"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/bus"
)

// TestAskNoParentGoesToOrchestrator asks from a job that has no parent. The
// question reaches the orchestrator once, and it names the question id.
func TestAskNoParentGoesToOrchestrator(t *testing.T) {
	reg, _ := withTestWiring(t)
	child, release := startLiveJob(t, reg)
	defer release()

	qid, ok := jobAskerAdapter{reg: reg}.Ask(context.Background(), child, "Which branch?")
	if !ok || !strings.HasPrefix(qid, "q") {
		t.Fatalf("Ask = %q, %v, want a question id", qid, ok)
	}

	got := drainNotices()
	if len(got) != 1 || !strings.Contains(got[0], "Which branch?") || !strings.Contains(got[0], "question id="+qid) {
		t.Fatalf("orchestrator drain = %q, want the question once with id %s", got, qid)
	}
}

// TestAnswerAcceptsQID answers a question by its id. The answer is an
// ask.reply to the asking child. A second answer to the same id is an error.
func TestAnswerAcceptsQID(t *testing.T) {
	reg, _ := withTestWiring(t)
	child, release := startLiveJob(t, reg)
	defer release()
	replies := appBus.Subscribe("test-ask-replies", bus.Filter{To: agentAddr(child), Kinds: []bus.Kind{bus.KindAskReply}})
	defer replies.Close()

	qid, ok := jobAskerAdapter{reg: reg}.Ask(context.Background(), child, "Which branch?")
	if !ok {
		t.Fatal("Ask failed")
	}
	answerer := jobAnswererAdapter{reg: reg}
	if err := answerer.Answer(qid, "use main"); err != nil {
		t.Fatalf("Answer(%s): %v", qid, err)
	}
	if err := answerer.Answer(qid, "again"); err == nil {
		t.Fatalf("second Answer(%s) returned no error", qid)
	}

	msgs := replies.Drain()
	if len(msgs) != 1 {
		t.Fatalf("child got %d ask.reply messages, want 1", len(msgs))
	}
	reply, err := bus.Decode[bus.AskReply](msgs[0])
	if err != nil || reply.Answer != "use main" {
		t.Fatalf("reply = %+v, %v, want the answer once", reply, err)
	}
}

// TestAnswerUnknownIDIsError answers ids that were never asked. Each returns an
// error and nothing is published.
func TestAnswerUnknownIDIsError(t *testing.T) {
	reg, _ := withTestWiring(t)
	answerer := jobAnswererAdapter{reg: reg}
	for _, id := range []string{"q999999", "job-1", "q", "qx"} {
		if err := answerer.Answer(id, "text"); err == nil {
			t.Fatalf("Answer(%q) returned no error", id)
		}
	}
}
