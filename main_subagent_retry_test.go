package main

import (
	"context"
	"testing"

	"github.com/crazy-goat/tyci-agent/api"
	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/tools"
)

// #447: a role agent (subagent) must survive a provider that stalls twice
// before it answers. The child used to run with MaxRetries 1, so the second
// stall ended the run and the workflow waited in ask for a human.
func TestAgentRunnerRun_RetriesStalledProviderBeforeAnswering(t *testing.T) {
	// A real stall waits BaseBackoff (4 s) before each retry. RetryAfter "0"
	// removes that wait; the test checks the retry count, not the backoff.
	stall := func() error {
		return &api.RetryableError{Message: "no answer from stub.example after 30s", RetryAfter: "0"}
	}
	fake := &connectortest.Flaky{
		Client:   connectortest.Text("answered after two stalls"),
		Failures: []connectortest.Failure{{Err: stall()}, {Err: stall()}},
	}
	ctx := connector.WithModelClient(context.Background(), fake)

	text, err := (&agentRunner{}).run(ctx, "do the thing", "", "", tools.SubagentOptions{})
	if err != nil {
		t.Fatalf("run after two stalls: %v", err)
	}
	if text != "answered after two stalls" {
		t.Fatalf("text = %q, want the answer after the stalls", text)
	}
	if got := fake.Calls(); got != 3 {
		t.Fatalf("provider calls = %d, want 3 (two stalls, then the answer)", got)
	}
}
