package main

import (
	"context"
	"strings"
	"testing"

	"github.com/decodo/tyci/tools"
)

// TestSubagentToolRunner_NoWhitelistAllowsEverythingButSubagent locks in
// today's default (allowed empty/nil): every real tool passes through, and
// "subagent" is still hard-denied regardless of any whitelist — recursion
// into child subagents is never permitted.
//
// Run's own depth-derived check only denies "subagent" for a caller NOT at
// depth 0 (item 21) — subagentToolRunner.Run always represents a real
// child's own tool dispatch in production, i.e. always depth >= 1, so this
// test wraps ctx with tools.WithDepth(..., 1) to match that instead of
// relying on context.Background()'s default depth 0, which item 21 makes a
// legitimate (top-level) depth for "subagent".
func TestSubagentToolRunner_NoWhitelistAllowsEverythingButSubagent(t *testing.T) {
	r := &subagentToolRunner{}
	ctx := tools.WithDepth(context.Background(), 1)

	if _, err := r.Run(ctx, "todo", map[string]any{"action": "list"}); err != nil {
		t.Errorf("expected \"todo\" to be allowed with no whitelist, got error: %v", err)
	}

	_, err := r.Run(ctx, "subagent", map[string]any{"task": "x"})
	if err == nil {
		t.Fatal("expected \"subagent\" to always be denied, even with no whitelist")
	}
	if !strings.Contains(err.Error(), "recursion denied") {
		t.Errorf("expected a recursion-denied error, got %v", err)
	}
}

// TestSubagentToolRunner_WhitelistPermitsListedTool is the positive half of
// runtime enforcement: a tool that IS in allowed must go through to the
// global registry.
func TestSubagentToolRunner_WhitelistPermitsListedTool(t *testing.T) {
	r := &subagentToolRunner{allowed: []string{"todo"}}

	if _, err := r.Run(context.Background(), "todo", map[string]any{"action": "list"}); err != nil {
		t.Errorf("expected \"todo\" to be allowed by the whitelist, got error: %v", err)
	}
}

// TestSubagentToolRunner_WhitelistDeniesUnlistedTool is the real security
// boundary this step adds: the schema handed to the model
// (tools.GetSubagentToolsSchemaJSONFor) is only a hint about what the model
// was offered — a model can still emit a call for a tool outside that list
// (stale cached tool list, hallucinated name). subagentToolRunner.Run is the
// actual gate, so it must reject a tool that isn't in allowed even though
// the tool itself is perfectly real and registered.
//
// The refusal comes from tools.AllowOnly, not a hand-rolled message here —
// see TestSubagentToolRunner_WhitelistAllowsAlwaysAllowedTools for why the
// runtime check now delegates to that same gate the schema builder uses.
func TestSubagentToolRunner_WhitelistDeniesUnlistedTool(t *testing.T) {
	r := &subagentToolRunner{allowed: []string{"read"}} // "todo" is not listed

	_, err := r.Run(context.Background(), "todo", map[string]any{"action": "list"})
	if err == nil {
		t.Fatal("expected \"todo\" to be denied by a whitelist that only lists \"read\"")
	}
	if !strings.Contains(err.Error(), "not available to this agent") {
		t.Errorf("expected a \"not available to this agent\" error, got %v", err)
	}
}

// TestSubagentToolRunner_WhitelistAllowsAlwaysAllowedTools is the regression
// test for the bug this fix closes: tools.GetSubagentToolsSchemaJSONFor
// always folds alwaysAllowedTools ("help", "lua") into the schema offered to
// a whitelisted child, whatever its own tools: list says — but Run used to
// enforce a hand-rolled loop over only r.allowed, so a child offered "help"
// in its schema got refused the moment it actually called it. Run now
// checks calls against tools.AllowOnlySubagent, the same gate that already
// folds in alwaysAllowedTools for the "lua" dispatch path below, so the
// runtime check agrees with the schema.
func TestSubagentToolRunner_WhitelistAllowsAlwaysAllowedTools(t *testing.T) {
	r := &subagentToolRunner{allowed: []string{"read"}} // neither "help" nor "lua" listed

	if _, err := r.Run(context.Background(), "help", map[string]any{}); err != nil {
		t.Errorf("expected \"help\" to be allowed even though it is not in the whitelist, got error: %v", err)
	}
}

// TestSubagentToolRunner_WhitelistStillDeniesSubagent covers the "someone
// wrote tools: read, subagent in an agent's frontmatter" case: even if
// "subagent" were present in allowed, the explicit recursion check in Run
// fires before the whitelist membership check.
func TestSubagentToolRunner_WhitelistStillDeniesSubagent(t *testing.T) {
	r := &subagentToolRunner{allowed: []string{"subagent"}}

	_, err := r.Run(tools.WithDepth(context.Background(), 1), "subagent", map[string]any{"task": "x"})
	if err == nil {
		t.Fatal("expected \"subagent\" to be denied even when explicitly whitelisted")
	}
	if !strings.Contains(err.Error(), "recursion denied") {
		t.Errorf("expected a recursion-denied error, got %v", err)
	}
}

// TestSubagentToolRunner_WhitelistDeniesAgentsEvenIfListed is the regression
// test for the "schema vs. runtime gate disagree" bug: tools.GetSubagentToolsSchema
// (and GetSubagentToolsSchemaJSONFor) never offer "agents" in a subagent's
// schema, whatever its tools: list says — its only purpose is discovering
// names for subagent(agent="name"), useless to a child that can't call
// subagent at all — but before this fix, Run's whitelist check permitted a
// call to "agents" whenever it was explicitly listed, since neither
// tools.AllowOnly nor the old hand-rolled loop filtered it out. A stray or
// hallucinated call to "agents" must be refused the same way the schema
// already hides it.
func TestSubagentToolRunner_WhitelistDeniesAgentsEvenIfListed(t *testing.T) {
	r := &subagentToolRunner{allowed: []string{"read", "agents"}}

	_, err := r.Run(context.Background(), "agents", map[string]any{})
	if err == nil {
		t.Fatal("expected \"agents\" to be denied even when explicitly whitelisted, to match the schema that never offers it")
	}
	if !strings.Contains(err.Error(), "not available to this agent") {
		t.Errorf("expected a \"not available to this agent\" error, got %v", err)
	}
}

// TestSubagentToolRunner_WhitelistOfOnlyDeniedNamesStillAllowsAlwaysAllowed
// pins the edge case tools.AllowOnlySubagent exists for: filtering
// []string{"agents"} through subagentDeniedTools leaves an EMPTY list, and
// a bare tools.AllowOnly(filtered...) call treats an empty names slice as
// "no restriction" (see AllowOnly's doc comment) — which would have handed
// this degenerate whitelist the run of the whole tool registry instead of
// just alwaysAllowedTools. AllowOnlySubagent must not fall into that trap:
// "help" (always allowed) still goes through, "read" (never listed, not
// always-allowed) is still denied.
func TestSubagentToolRunner_WhitelistOfOnlyDeniedNamesStillAllowsAlwaysAllowed(t *testing.T) {
	r := &subagentToolRunner{allowed: []string{"agents"}}

	if _, err := r.Run(context.Background(), "help", map[string]any{}); err != nil {
		t.Errorf("expected \"help\" to be allowed (alwaysAllowedTools) even though the whole whitelist was denied names, got error: %v", err)
	}
	if _, err := r.Run(context.Background(), "read", map[string]any{"path": "x"}); err == nil {
		t.Fatal("expected \"read\" to be denied — a whitelist of only [\"agents\"] must not fall through to \"everything is allowed\"")
	}
}

// TestSubagentToolRunner_NoWhitelistDeniesAgents is the regression test for
// the second half of the schema/gate mismatch: an UNRESTRICTED child (no
// tools: whitelist at all, opts.Tools empty, gate == nil in Run) used to
// have its context gate deny only "subagent", not "agents" — so a plain
// subagent(prompt=...) child could still reach "agents" (directly, or via
// lua's tool("agents", {})) even though GetSubagentToolsSchemaJSON never
// offers it. tools.DenySubagentRecursion must cover "agents" on this path
// too, not just the whitelisted one.
func TestSubagentToolRunner_NoWhitelistDeniesAgents(t *testing.T) {
	r := &subagentToolRunner{} // no whitelist at all

	_, err := r.Run(context.Background(), "agents", map[string]any{})
	if err == nil {
		t.Fatal("expected \"agents\" to be denied even with no whitelist, matching the schema that never offers it to a subagent")
	}
	if !strings.Contains(err.Error(), "recursion/discovery denied") {
		t.Errorf("expected a recursion/discovery-denied error, got %v", err)
	}
}
