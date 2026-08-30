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

// TestRunTask_ScoutMode_PromptAndSchemaAgreeAtEveryDepth is the end-to-end
// wiring test for F31's round-2 finding: DelegationLineMatchesSchema
// (providers package) proves BuildScoutSystemPrompt itself is correct given
// a canSpawnScout bool, but nothing at the level below caught main.go's
// RunTask computing that bool from the WRONG depth (e.g. an off-by-one)
// before this test existed — TestRunTask_ScoutMode_UsesScoutPrompt only ever
// runs at depth 0 (a bare context.Background()), which is a depth where the
// prompt and the schema trivially agree (neither offers "scout"), so it
// could never catch a mismatch either.
//
// This drives agentRunner.RunTask across depth 0..4, which spans every depth
// a scout's OWN depth can take (2..4) plus both neighbours — depth 0 and 1
// are a scout's caller, tested here for branch coverage of the predicate.
// (See tools.AllowedDelegationTool's doc comment: depth 0
// is the top level, 1..3 is where a scout may itself spawn one more scout,
// 4 is past the cap) and asserts the ONE invariant that actually matters:
// whatever the wire schema says about "scout" being offered, the system
// prompt's own "can you spawn another scout" line must say the same thing.
// schemaToolNames is the existing helper from main_delegation_depth_test.go
// (same package).
func TestRunTask_ScoutMode_PromptAndSchemaAgreeAtEveryDepth(t *testing.T) {
	for depth := 0; depth <= 4; depth++ {
		fake := connectortest.Text("scout answer")
		ctx := connector.WithModelClient(context.Background(), fake)
		ctx = tools.WithDepth(ctx, depth)

		r := &agentRunner{}
		if _, err := r.RunTask(ctx, "look something up", "", tools.SubagentOptions{ScoutMode: true}); err != nil {
			t.Fatalf("depth %d: RunTask: %v", depth, err)
		}

		reqs := fake.Requests()
		if len(reqs) != 1 {
			t.Fatalf("depth %d: expected 1 request, got %d", depth, len(reqs))
		}
		req := reqs[0]

		promptOffersScout := strings.Contains(req.System, "scout(task)")
		schemaOffersScout := schemaToolNames(t, req.Tools)["scout"]
		if promptOffersScout != schemaOffersScout {
			t.Errorf("depth %d: prompt says scout(task) available = %v, schema actually offers \"scout\" = %v — these must agree:\nSystem:\n%s",
				depth, promptOffersScout, schemaOffersScout, req.System)
		}
	}
}
