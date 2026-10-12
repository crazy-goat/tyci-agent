package main

import (
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/bus"
)

// TestBusKindWatchSeesOnlyTheKinds: the watch reports an ask.reply sent to the
// job, and ignores another notice to the same job.
func TestBusKindWatchSeesOnlyTheKinds(t *testing.T) {
	reg, _ := withTestWiring(t)
	job, release := startLiveJob(t, reg)
	defer release()

	arrived, stop := busKindWatch(job, []string{"ask.reply"})

	publishAgentMessage(appBus, job, orchestratorAddr, bus.OriginAgent, "hello")
	if arrived() {
		t.Fatal("an agent.message woke a watch for ask.reply")
	}

	seq, err := bus.PublishReply(appBus, bus.KindAskReply, orchestratorAddr, agentAddr(job), bus.OriginAgent,
		1, bus.AskReply{Question: "q1", Answer: "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if !arrived() {
		t.Fatal("an ask.reply to the job did not wake the watch")
	}

	if got := agentInboxes.drain(job); len(got) != 2 || !strings.Contains(got[1], "yes") {
		t.Fatalf("job inbox = %q, want the message and the reply", got)
	}
	stop()
	// The last event of the reply holds its final state.
	var last *bus.Event
	for _, e := range appBus.Events() {
		if e.Msg.Seq == seq {
			last = &e
		}
	}
	if last == nil || last.State != bus.Received {
		t.Fatalf("last state of the reply = %+v, want Received after the job drained it and the watch stopped", last)
	}
}
