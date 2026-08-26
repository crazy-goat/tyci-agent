package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withHermeticAgentsDirs points both the global (~/.tyci/agents) and
// project-local (<cwd>/.tyci/agents) directories AgentsTool reads at empty
// temp dirs, then writes one project-local agent definition into it.
// Mirrors the os.Chdir + t.Setenv("HOME", ...) pattern already used in
// providers/provider_test.go and internal/agentdefs/agentdefs_test.go's
// TestGetAndList_Hermetic — needed because AgentsTool.Run calls
// agentdefs.List("")/Get("", ...) with an empty wd, which resolves against
// the real HOME and os.Getwd() otherwise.
func withHermeticAgentsDir(t *testing.T) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	wd := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	agentsDir := filepath.Join(wd, ".tyci", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	body := `---
description: reviews code for correctness
model: reviewer-model
tools: read,find
max_iterations: 5
---
You are a careful reviewer.`
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentsTool_ListsAvailableAgents(t *testing.T) {
	withHermeticAgentsDir(t)

	res := (&AgentsTool{}).Run(context.Background(), map[string]any{})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "reviewer") || !strings.Contains(res.Content, "reviews code for correctness") {
		t.Errorf("expected listing to name the agent and its description, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "[tools: read, find]") {
		t.Errorf("expected listing to show the agent's tool allowlist, got: %s", res.Content)
	}
}

func TestAgentsTool_LoadsOneAgentsFullDefinition(t *testing.T) {
	withHermeticAgentsDir(t)

	res := (&AgentsTool{}).Run(context.Background(), map[string]any{"name": "reviewer"})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	for _, want := range []string{"reviewer-model", "read, find", "You are a careful reviewer.", "Max iterations: 5 (legacy field; ignored for subagents; execution is unlimited)"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("expected loaded definition to contain %q, got: %s", want, res.Content)
		}
	}
}

func TestAgentsTool_UnknownNameErrors(t *testing.T) {
	withHermeticAgentsDir(t)

	res := (&AgentsTool{}).Run(context.Background(), map[string]any{"name": "does-not-exist"})
	if res.Success {
		t.Fatal("expected an unknown agent name to fail, not silently succeed")
	}
	if !strings.Contains(res.Error, "does-not-exist") {
		t.Errorf("expected the error to name the missing agent, got: %s", res.Error)
	}
}

func TestAgentsTool_NoAgentsDefinedReturnsHelpfulMessage(t *testing.T) {
	home := t.TempDir()
	wd := t.TempDir()
	t.Setenv("HOME", home)
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	res := (&AgentsTool{}).Run(context.Background(), map[string]any{})
	if !res.Success {
		t.Fatalf("expected success (empty list is not an error), got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "No named agents available") {
		t.Errorf("expected a helpful empty-state message, got: %s", res.Content)
	}
}
