package main

import (
	"context"
	"strings"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/tools"
)

// TestRunTask_ScoutMode_UsesScoutPrompt is F31 (TODO.md): a scout's system
// prompt must come from providers.BuildScoutSystemPrompt, not
// BuildSubagentSystemPrompt — the latter advertises tools (todo, lua, bash,
// memory, cron, ...) a scout does not have and tools.ScoutGate refuses. This
// asserts the fix at the actual seam agentRunner.RunTask uses to pick a
// system prompt (opts.ScoutMode), the same seam agentRunner.run already
// consults to pick ledger.Scout vs ledger.Subagent — see main.go's run,
// "opts.ScoutMode" near the ledger.Kind assignment.
func TestRunTask_ScoutMode_UsesScoutPrompt(t *testing.T) {
	fake := connectortest.Text("scout answer")
	ctx := connector.WithModelClient(context.Background(), fake)

	r := &agentRunner{}
	if _, err := r.RunTask(ctx, "find the config loader", "", tools.SubagentOptions{ScoutMode: true}); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	got := reqs[0].System
	if !strings.Contains(got, "You are tyci's scout") {
		t.Errorf("expected the scout prompt, got:\n%s", got)
	}
	if strings.Contains(got, "You are a SUBAGENT spawned by a parent agent") {
		t.Errorf("expected the scout prompt to NOT contain the ordinary subagent contract, got:\n%s", got)
	}
}

// TestRunTask_NoScoutMode_UsesSubagentPrompt is the complementary case: an
// ordinary subagent (opts.ScoutMode false, the zero value) must keep getting
// the standard subagent contract — this is the pre-existing behavior F31
// must not regress.
func TestRunTask_NoScoutMode_UsesSubagentPrompt(t *testing.T) {
	fake := connectortest.Text("subagent answer")
	ctx := connector.WithModelClient(context.Background(), fake)

	r := &agentRunner{}
	if _, err := r.RunTask(ctx, "do the thing", "", tools.SubagentOptions{}); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	got := reqs[0].System
	if !strings.Contains(got, "You are a SUBAGENT spawned by a parent agent") {
		t.Errorf("expected the ordinary subagent contract, got:\n%s", got)
	}
	if strings.Contains(got, "You are tyci's scout") {
		t.Errorf("expected NOT the scout prompt for a plain subagent, got:\n%s", got)
	}
}
