package main

import (
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
	t.Cleanup(stop)

	publishAgentMessage(appBus, job, orchestratorAddr, bus.OriginAgent, "hello")
	if arrived() {
		t.Fatal("an agent.message woke a watch for ask.reply")
	}

	if _, err := bus.PublishReply(appBus, bus.KindAskReply, orchestratorAddr, agentAddr(job), bus.OriginAgent,
		1, bus.AskReply{Question: "q1", Answer: "yes"}); err != nil {
		t.Fatal(err)
	}
	if !arrived() {
		t.Fatal("an ask.reply to the job did not wake the watch")
	}
}
