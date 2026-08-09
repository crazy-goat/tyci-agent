package main

import (
	"context"
	"strings"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/tools"
)

// These tests cover agentRunner.RunTaskWithSystem's dispatch on
// opts.SystemPromptMode, end to end down to the wire request recorded by
// connectortest.Fake — the composition this whole refactor is about: a named
// agent's markdown body must no longer silently discard the subagent
// contract and environment context unless it explicitly opts into "replace".

// TestRunTaskWithSystem_ReplaceMode_UsesBodyVerbatim locks in the
// pre-existing behavior for definitions that explicitly opt in: `system` on
// the wire is exactly the body, nothing added, nothing removed.
func TestRunTaskWithSystem_ReplaceMode_UsesBodyVerbatim(t *testing.T) {
	fake := connectortest.Text("child answer")
	ctx := connector.WithModelClient(context.Background(), fake)

	body := "You are a laser-focused reviewer. Only report bugs."
	opts := tools.SubagentOptions{SystemPromptMode: "replace"}

	r := &agentRunner{}
	if _, err := r.RunTaskWithSystem(ctx, "do the thing", "", body, opts); err != nil {
		t.Fatalf("RunTaskWithSystem: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].System != body {
		t.Errorf("System = %q, want exactly the body %q", reqs[0].System, body)
	}
}

// TestRunTaskWithSystem_AppendMode_IncludesBodyAndContract is the fix this
// task exists for: in append mode (the default), the wire's System prompt
// must contain BOTH the definition's body (as a role) AND the standard
// subagent contract, so a named agent keeps the harness's guardrails instead
// of losing them to a hand-written body.
func TestRunTaskWithSystem_AppendMode_IncludesBodyAndContract(t *testing.T) {
	fake := connectortest.Text("child answer")
	ctx := connector.WithModelClient(context.Background(), fake)

	body := "You are a laser-focused reviewer. Only report bugs."
	opts := tools.SubagentOptions{SystemPromptMode: "append"}

	r := &agentRunner{}
	if _, err := r.RunTaskWithSystem(ctx, "do the thing", "", body, opts); err != nil {
		t.Fatalf("RunTaskWithSystem: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	got := reqs[0].System
	if !strings.Contains(got, body) {
		t.Errorf("expected System to contain the body %q, got:\n%s", body, got)
	}
	if !strings.Contains(got, "You are a SUBAGENT spawned by a parent agent") {
		t.Errorf("expected System to contain the subagent contract, got:\n%s", got)
	}
	if got == body {
		t.Error("expected System to be more than just the body in append mode")
	}
}

// TestRunTaskWithSystem_EmptyModeDefaultsToAppend checks the safe-default
// branch explicitly: an empty opts.SystemPromptMode (a caller that bypasses
// agentdefs, or an old/unset value) must NOT fall back to "replace" — that
// was the harmful pre-existing behavior this refactor removes.
func TestRunTaskWithSystem_EmptyModeDefaultsToAppend(t *testing.T) {
	fake := connectortest.Text("child answer")
	ctx := connector.WithModelClient(context.Background(), fake)

	body := "You are a laser-focused reviewer."
	opts := tools.SubagentOptions{} // SystemPromptMode left unset

	r := &agentRunner{}
	if _, err := r.RunTaskWithSystem(ctx, "do the thing", "", body, opts); err != nil {
		t.Fatalf("RunTaskWithSystem: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	got := reqs[0].System
	if !strings.Contains(got, body) || !strings.Contains(got, "You are a SUBAGENT spawned by a parent agent") {
		t.Errorf("expected an empty SystemPromptMode to default to append (body + contract), got:\n%s", got)
	}
}
