package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAgentDef writes a minimal agent markdown definition file into dir.
func writeAgentDef(t *testing.T, dir, name, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	fm := "---\n"
	if description != "" {
		fm += "description: " + description + "\n"
	}
	fm += "---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(fm), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestBuildSystemPrompt_listsAgents: top-level prompt must surface a
// project-defined agent's name and description so the model can discover
// the subagent tool's agent parameter without guessing a filename.
func TestBuildSystemPrompt_listsAgents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	t.Chdir(tmp)

	writeAgentDef(t, filepath.Join(tmp, ".tyci", "agents"), "reviewer", "Reviews Go diffs for correctness")

	prompt := BuildSystemPrompt()
	if !strings.Contains(prompt, "Available agents for subagent(agent=\"name\"):") {
		t.Fatalf("prompt missing agents header:\n%s", prompt)
	}
	if !strings.Contains(prompt, "reviewer — Reviews Go diffs for correctness") {
		t.Errorf("prompt missing agent name+description line:\n%s", prompt)
	}
}

// TestBuildSubagentSystemPrompt_omitsAgents: children cannot spawn further
// children, so listing agents in a subagent's own prompt would tempt it to
// call a tool it does not have.
func TestBuildSubagentSystemPrompt_omitsAgents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	t.Chdir(tmp)

	writeAgentDef(t, filepath.Join(tmp, ".tyci", "agents"), "reviewer", "Reviews Go diffs for correctness")

	prompt := BuildSubagentSystemPrompt()
	if strings.Contains(prompt, "Available agents") {
		t.Errorf("subagent prompt should not list agents, but it does:\n%s", prompt)
	}
}

// TestBuildSystemPrompt_noAgents_noHeader: with no agent definitions
// anywhere, the prompt must carry no agents section at all.
func TestBuildSystemPrompt_noAgents_noHeader(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	t.Chdir(tmp)

	prompt := BuildSystemPrompt()
	if strings.Contains(prompt, "Available agents") {
		t.Errorf("expected no agents section when none are defined:\n%s", prompt)
	}
}

// TestBuildSystemPrompt_agentWithoutDescription: an agent with no
// description renders as just its name, with no dangling " — " separator.
func TestBuildSystemPrompt_agentWithoutDescription(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	t.Chdir(tmp)

	writeAgentDef(t, filepath.Join(tmp, ".tyci", "agents"), "explorer", "")

	prompt := BuildSystemPrompt()
	if !strings.Contains(prompt, "- explorer\n") {
		t.Errorf("expected bare agent name line without description:\n%s", prompt)
	}
	if strings.Contains(prompt, "explorer —") {
		t.Errorf("agent without description should not have a dangling separator:\n%s", prompt)
	}
}

// TestBuildSystemPrompt_projectAgentOverridesGlobal: a project-local agent
// definition with the same name as a global one wins, and only one entry
// appears in the prompt.
func TestBuildSystemPrompt_projectAgentOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tmp := t.TempDir()
	t.Chdir(tmp)

	writeAgentDef(t, filepath.Join(home, ".tyci", "agents"), "reviewer", "Global description")
	writeAgentDef(t, filepath.Join(tmp, ".tyci", "agents"), "reviewer", "Project description")

	prompt := BuildSystemPrompt()
	if strings.Contains(prompt, "Global description") {
		t.Errorf("project agent should override global, but global description leaked:\n%s", prompt)
	}
	if !strings.Contains(prompt, "reviewer — Project description") {
		t.Errorf("expected project agent's description to win:\n%s", prompt)
	}
	if strings.Count(prompt, "\n- reviewer") != 1 {
		t.Errorf("expected exactly one 'reviewer' entry, got prompt:\n%s", prompt)
	}
}
