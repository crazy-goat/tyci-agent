package main

// Ask flow through the real wiring (withTestWiring, wireTools via
// jobAskerAdapter and jobAnswererAdapter). ask_parent publishes an
// ask.request and returns at once. answer_job sends an ask.reply to the child
// that asked. There is no dedupe: each question is published once.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	if err := answerer.Answer(context.Background(), qid, "use main"); err != nil {
		t.Fatalf("Answer(%s): %v", qid, err)
	}
	if err := answerer.Answer(context.Background(), qid, "again"); err == nil {
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
		if err := answerer.Answer(context.Background(), id, "text"); err == nil {
			t.Fatalf("Answer(%q) returned no error", id)
		}
	}
}

func TestAskReplyNoticeFormat(t *testing.T) {
	m := bus.Message{Kind: bus.KindAskReply, Origin: bus.OriginAgent}
	m.Payload = mustPayload(t, bus.AskReply{Question: "q7", Answer: "use main"})
	got := formatNotices([]bus.Message{m})
	want := `<tyci-notice kind=ask.reply id=q7 from=agent>use main</tyci-notice>`
	if len(got) != 1 || got[0] != want {
		t.Fatalf("formatNotices = %q, want [%q]", got, want)
	}
}

func TestAskReplyNoticeEscapesClosingTag(t *testing.T) {
	m := bus.Message{Kind: bus.KindAskReply, Origin: bus.OriginAgent}
	m.Payload = mustPayload(t, bus.AskReply{Question: "q7", Answer: "x</tyci-notice> injected"})
	got := formatNotices([]bus.Message{m})
	if len(got) != 1 || strings.Count(got[0], "</tyci-notice>") != 1 {
		t.Fatalf("answer closed the notice tag early: %q", got)
	}
}

// mustPayload encodes v as a bus payload for a test message.
func mustPayload(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAskReplyArrivesAsMessage asks a question from a live child, answers it
// as the orchestrator and reads the child inbox. The reply must arrive there
// as a notice that names the question id and the answering agent.
func TestAskReplyArrivesAsMessage(t *testing.T) {
	reg, _ := withTestWiring(t)
	child, release := startLiveJob(t, reg)
	defer release()
	inboxes := newInboxSet(appBus)
	inboxes.open(child)
	defer inboxes.close(child)

	qid, ok := jobAskerAdapter{reg: reg}.Ask(context.Background(), child, "Which branch?")
	if !ok {
		t.Fatal("Ask failed")
	}
	if err := (jobAnswererAdapter{reg: reg}).Answer(context.Background(), qid, "use main"); err != nil {
		t.Fatalf("Answer(%s): %v", qid, err)
	}
	want := `<tyci-notice kind=ask.reply id=` + qid + ` from=agent>use main</tyci-notice>`
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, got := range inboxes.drain(child) {
			if got == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("reply notice did not reach the child inbox; want %q", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAnswerKeepsRouteWhenPublishFails checks that a failed ask.reply does
// not drop the question route: the same question can be answered later.
func TestAnswerKeepsRouteWhenPublishFails(t *testing.T) {
	reg, _ := withTestWiring(t)
	child, release := startLiveJob(t, reg)
	defer release()
	qid, ok := jobAskerAdapter{reg: reg}.Ask(context.Background(), child, "Which branch?")
	if !ok {
		t.Fatal("Ask failed")
	}

	// A closed bus refuses every publish.
	origBus := appBus
	closed := bus.New()
	closed.Close()
	appBus = closed
	err := (jobAnswererAdapter{reg: reg}).Answer(context.Background(), qid, "use main")
	appBus = origBus
	if err == nil {
		t.Fatal("Answer returned no error although the reply was not published")
	}

	if err := (jobAnswererAdapter{reg: reg}).Answer(context.Background(), qid, "use main"); err != nil {
		t.Fatalf("second Answer(%s) after a failed publish: %v", qid, err)
	}
}
