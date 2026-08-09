package main

import (
	"context"
	"strings"
	"testing"
)

// TestSubagentToolRunner_NoWhitelistAllowsEverythingButSubagent locks in
// today's default (allowed empty/nil): every real tool passes through, and
// "subagent" is still hard-denied regardless of any whitelist — recursion
// into child subagents is never permitted.
func TestSubagentToolRunner_NoWhitelistAllowsEverythingButSubagent(t *testing.T) {
	r := &subagentToolRunner{}

	if _, err := r.Run(context.Background(), "todo", map[string]any{"action": "list"}); err != nil {
		t.Errorf("expected \"todo\" to be allowed with no whitelist, got error: %v", err)
	}

	_, err := r.Run(context.Background(), "subagent", map[string]any{"task": "x"})
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
func TestSubagentToolRunner_WhitelistDeniesUnlistedTool(t *testing.T) {
	r := &subagentToolRunner{allowed: []string{"read"}} // "todo" is not listed

	_, err := r.Run(context.Background(), "todo", map[string]any{"action": "list"})
	if err == nil {
		t.Fatal("expected \"todo\" to be denied by a whitelist that only lists \"read\"")
	}
	if !strings.Contains(err.Error(), "not in this agent's allowed tools list") {
		t.Errorf("expected an \"allowed tools list\" error, got %v", err)
	}
}

// TestSubagentToolRunner_WhitelistStillDeniesSubagent covers the "someone
// wrote tools: read, subagent in an agent's frontmatter" case: even if
// "subagent" were present in allowed, the explicit recursion check in Run
// fires before the whitelist membership check.
func TestSubagentToolRunner_WhitelistStillDeniesSubagent(t *testing.T) {
	r := &subagentToolRunner{allowed: []string{"subagent"}}

	_, err := r.Run(context.Background(), "subagent", map[string]any{"task": "x"})
	if err == nil {
		t.Fatal("expected \"subagent\" to be denied even when explicitly whitelisted")
	}
	if !strings.Contains(err.Error(), "recursion denied") {
		t.Errorf("expected a recursion-denied error, got %v", err)
	}
}
