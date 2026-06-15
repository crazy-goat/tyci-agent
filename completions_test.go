package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func writeTyciConfig(t *testing.T, modelJSON, agentsJSON string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	tyciDir := filepath.Join(dir, ".tyci")
	if err := os.MkdirAll(tyciDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if modelJSON != "" {
		if err := os.WriteFile(filepath.Join(tyciDir, "model.json"), []byte(modelJSON), 0o644); err != nil {
			t.Fatalf("write model.json: %v", err)
		}
	}
	if agentsJSON != "" {
		if err := os.WriteFile(filepath.Join(tyciDir, "agents.json"), []byte(agentsJSON), 0o644); err != nil {
			t.Fatalf("write agents.json: %v", err)
		}
	}
	t.Setenv("TYCI_MODEL_JSON", filepath.Join(tyciDir, "model.json"))
}

func TestCompleteProviderModels_Empty(t *testing.T) {
	writeTyciConfig(t, `{}`, "")

	out, directive := completeProviderModels(nil, nil, "")
	if len(out) != 0 {
		t.Errorf("expected empty completions, got %v", out)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("expected ShellCompDirectiveNoFileComp, got %d", directive)
	}
}

func TestCompleteProviderModels_Multiple(t *testing.T) {
	writeTyciConfig(t, `{
		"openai": {
			"gpt-4o":      {"uri": "openai://gpt-4o@$KEY@example/v1"},
			"gpt-4o-mini": {"uri": "openai://gpt-4o-mini@$KEY@example/v1"}
		},
		"anthropic": {
			"claude-3-5-sonnet-latest": {"uri": "anthropic://claude-3-5-sonnet-latest@$KEY@example/v1"}
		}
	}`, "")

	out, _ := completeProviderModels(nil, nil, "")
	want := map[string]bool{
		"openai/gpt-4o":                      true,
		"openai/gpt-4o-mini":                 true,
		"anthropic/claude-3-5-sonnet-latest": true,
	}
	if len(out) != len(want) {
		t.Fatalf("expected %d completions, got %d: %v", len(want), len(out), out)
	}
	for _, v := range out {
		if !want[v] {
			t.Errorf("unexpected completion: %q", v)
		}
	}
}

func TestCompleteProviderModels_PrefixFilter(t *testing.T) {
	writeTyciConfig(t, `{
		"openai":     {"gpt-4o": {"uri": "openai://gpt-4o@$KEY@v1"}},
		"anthropic":  {"claude-3-5-sonnet-latest": {"uri": "anthropic://claude-3-5-sonnet-latest@$KEY@v1"}}
	}`, "")

	out, _ := completeProviderModels(nil, nil, "anth")
	if len(out) != 1 || out[0] != "anthropic/claude-3-5-sonnet-latest" {
		t.Errorf("prefix filter: got %v, want [anthropic/claude-3-5-sonnet-latest]", out)
	}
}

func TestCompleteAgents_Empty(t *testing.T) {
	writeTyciConfig(t, "", `{}`)

	out, _ := completeAgents(nil, nil, "")
	if len(out) != 0 {
		t.Errorf("expected empty, got %v", out)
	}
}

func TestCompleteAgents_Multiple(t *testing.T) {
	writeTyciConfig(t, "", `{
		"default":       {"model": "openai/gpt-4o"},
		"code-reviewer": {"model": "anthropic/claude-3-5-sonnet-latest"}
	}`)

	out, _ := completeAgents(nil, nil, "")
	if len(out) != 2 {
		t.Fatalf("expected 2 agents, got %d: %v", len(out), out)
	}
	got := strings.Join(out, ",")
	for _, want := range []string{"default", "code-reviewer"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in completions: %v", want, out)
		}
	}
}

func TestCompleteProviderNames_Dedup(t *testing.T) {
	writeTyciConfig(t, `{"openai": {"gpt-4o": {"uri": "openai://gpt-4o@$KEY@v1"}}}`, "")

	out, _ := completeProviderNames(nil, nil, "")
	if len(out) != 1 || out[0] != "openai" {
		t.Errorf("expected [openai], got %v", out)
	}
}

// TestCompletionBinaryInvocation exercises the binary's __complete hidden
// command, which is what shell completion scripts call. Cobra prints one
// suggestion per line followed by a directive line.
func TestCompletionBinaryInvocation(t *testing.T) {
	bin := buildTyciBinary(t)
	writeTyciConfig(t, `{"openai": {"gpt-4o": {"uri": "openai://gpt-4o@$KEY@v1"}}}`, "")

	out := runTyciBinary(t, bin, "__complete", "run", "--model", "")
	if !strings.Contains(out, "openai/gpt-4o") {
		t.Errorf("expected openai/gpt-4o in output, got:\n%s", out)
	}
	if !strings.Contains(out, "ShellCompDirectiveNoFileComp") {
		t.Errorf("expected ShellCompDirectiveNoFileComp directive line, got:\n%s", out)
	}
}

func buildTyciBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "tyci")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func runTyciBinary(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run %v: %v\n%s", args, err, out)
	}
	return string(out)
}
