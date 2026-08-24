package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeMarkdownAgent writes a minimal markdown agent definition file named
// "<name>.md" into dir, creating dir if needed.
func writeMarkdownAgent(t *testing.T, dir, name, frontmatter, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "---\n" + frontmatter + "\n---\n" + body
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestAgentEntryMarshalNoFallback(t *testing.T) {
	// Without fallback → should marshal as plain string
	entry := AgentEntry{Model: "openai/gpt-4o"}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != `"openai/gpt-4o"` {
		t.Errorf("expected plain string, got %s", string(data))
	}
}

func TestAgentEntryMarshalWithFallback(t *testing.T) {
	// With fallback → should marshal as object
	entry := AgentEntry{
		Model:    "openai/gpt-4o",
		Fallback: []string{"anthropic/claude-opus", "google/gemini-ultra"},
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	if parsed["model"] != "openai/gpt-4o" {
		t.Errorf("expected model 'openai/gpt-4o', got %v", parsed["model"])
	}
	fb, ok := parsed["fallback"].([]any)
	if !ok {
		t.Fatalf("expected fallback array, got %T", parsed["fallback"])
	}
	if len(fb) != 2 || fb[0] != "anthropic/claude-opus" || fb[1] != "google/gemini-ultra" {
		t.Errorf("unexpected fallback: %v", fb)
	}
}

func TestAgentEntryUnmarshalOldFormat(t *testing.T) {
	// Old format: just a string
	var entry AgentEntry
	data := `"openai/gpt-4o"`
	if err := json.Unmarshal([]byte(data), &entry); err != nil {
		t.Fatalf("Unmarshal old format: %v", err)
	}
	if entry.Model != "openai/gpt-4o" {
		t.Errorf("expected model 'openai/gpt-4o', got %q", entry.Model)
	}
	if entry.Fallback != nil {
		t.Errorf("expected nil fallback, got %v", entry.Fallback)
	}
}

func TestAgentEntryUnmarshalNewFormat(t *testing.T) {
	// New format with fallback
	var entry AgentEntry
	data := `{"model": "openai/gpt-4o", "fallback": ["anthropic/claude-opus"]}`
	if err := json.Unmarshal([]byte(data), &entry); err != nil {
		t.Fatalf("Unmarshal new format: %v", err)
	}
	if entry.Model != "openai/gpt-4o" {
		t.Errorf("expected model 'openai/gpt-4o', got %q", entry.Model)
	}
	if len(entry.Fallback) != 1 || entry.Fallback[0] != "anthropic/claude-opus" {
		t.Errorf("expected fallback ['anthropic/claude-opus'], got %v", entry.Fallback)
	}
}

func TestAgentEntryUnmarshalNewFormatNoFallback(t *testing.T) {
	// New format object but no fallback field
	var entry AgentEntry
	data := `{"model": "openai/gpt-4o"}`
	if err := json.Unmarshal([]byte(data), &entry); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if entry.Model != "openai/gpt-4o" {
		t.Errorf("expected model 'openai/gpt-4o', got %q", entry.Model)
	}
	if entry.Fallback != nil {
		t.Errorf("expected nil fallback, got %v", entry.Fallback)
	}
}

func TestAgentsMapMarshalNoFallback(t *testing.T) {
	// Agents map with no fallback → should marshal as flat string map
	agents := Agents{
		"default": {Model: "openai/gpt-4o"},
		"coder":   {Model: "anthropic/claude-opus"},
	}
	data, err := json.MarshalIndent(agents, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// Values should be strings (not objects)
	for _, name := range []string{"default", "coder"} {
		v, ok := parsed[name].(string)
		if !ok {
			t.Errorf("expected string value for %q, got %T", name, parsed[name])
		} else if v == "" {
			t.Errorf("expected non-empty string for %q", name)
		}
	}
}

func TestAgentsMapMarshalWithFallback(t *testing.T) {
	// Agents map with fallback → should marshal as objects
	agents := Agents{
		"default": {Model: "openai/gpt-4o", Fallback: []string{"anthropic/claude-opus"}},
		"simple":  {Model: "openai/gpt-3.5"},
	}
	data, err := json.MarshalIndent(agents, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// "default" should be an object
	if _, ok := parsed["default"].(map[string]any); !ok {
		t.Errorf("expected object for 'default', got %T", parsed["default"])
	}
	// "simple" should still be a plain string
	if _, ok := parsed["simple"].(string); !ok {
		t.Errorf("expected string for 'simple', got %T", parsed["simple"])
	}
}

func TestAgentsMapUnmarshalMixedFormat(t *testing.T) {
	// Mixed old and new format in the same file
	data := `{
		"default": "openai/gpt-4o",
		"with-fb": {"model": "anthropic/claude-opus", "fallback": ["google/gemini-ultra"]}
	}`
	var agents Agents
	if err := json.Unmarshal([]byte(data), &agents); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if e, ok := agents["default"]; !ok {
		t.Error("missing 'default'")
	} else if e.Model != "openai/gpt-4o" {
		t.Errorf("expected model 'openai/gpt-4o', got %q", e.Model)
	} else if e.Fallback != nil {
		t.Errorf("expected nil fallback, got %v", e.Fallback)
	}

	if e, ok := agents["with-fb"]; !ok {
		t.Error("missing 'with-fb'")
	} else if e.Model != "anthropic/claude-opus" {
		t.Errorf("expected model 'anthropic/claude-opus', got %q", e.Model)
	} else if len(e.Fallback) != 1 || e.Fallback[0] != "google/gemini-ultra" {
		t.Errorf("expected fallback ['google/gemini-ultra'], got %v", e.Fallback)
	}
}

func TestSetFallbackAndGet(t *testing.T) {
	// Use a temp dir to avoid clobbering real config
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Set fallback models
	err := SetFallback("test-agent", []string{"provider/model1", "provider/model2"})
	if err != nil {
		t.Fatalf("SetFallback: %v", err)
	}

	// Verify via GetFallbackModels
	fb := GetFallbackModels("test-agent")
	if fb == nil {
		t.Fatal("GetFallbackModels returned nil")
	}
	if len(fb) != 2 || fb[0] != "provider/model1" || fb[1] != "provider/model2" {
		t.Errorf("unexpected fallback: %v", fb)
	}

	// Verify via GetAgentEntry
	entry, ok := GetAgentEntry("test-agent")
	if !ok {
		t.Fatal("GetAgentEntry returned false")
	}
	if entry.Model != "" {
		t.Errorf("expected empty model, got %q", entry.Model)
	}
	if len(entry.Fallback) != 2 || entry.Fallback[0] != "provider/model1" {
		t.Errorf("unexpected fallback in entry: %v", entry.Fallback)
	}
}

func TestSetFallbackRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Set first
	err := SetFallback("test", []string{"provider/model1"})
	if err != nil {
		t.Fatalf("SetFallback: %v", err)
	}
	if fb := GetFallbackModels("test"); len(fb) != 1 {
		t.Fatalf("expected 1 fallback, got %v", fb)
	}

	// Remove by passing empty
	err = SetFallback("test", nil)
	if err != nil {
		t.Fatalf("SetFallback remove: %v", err)
	}
	if fb := GetFallbackModels("test"); fb != nil {
		t.Errorf("expected nil fallback after removal, got %v", fb)
	}
}

func TestSetFallbackAgentDoesNotExist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// SetFallback on non-existent agent should create it
	err := SetFallback("new-agent", []string{"provider/model"})
	if err != nil {
		t.Fatalf("SetFallback: %v", err)
	}
	entry, ok := GetAgentEntry("new-agent")
	if !ok {
		t.Fatal("agent should have been created")
	}
	if entry.Model != "" {
		t.Errorf("expected empty model, got %q", entry.Model)
	}
	if len(entry.Fallback) != 1 || entry.Fallback[0] != "provider/model" {
		t.Errorf("unexpected fallback: %v", entry.Fallback)
	}
}

func TestSetAgentPreservesFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Set fallback first
	err := SetFallback("agent", []string{"provider/fb1"})
	if err != nil {
		t.Fatalf("SetFallback: %v", err)
	}

	// Then set model
	err = SetAgent("agent", "provider/main")
	if err != nil {
		t.Fatalf("SetAgent: %v", err)
	}

	// Fallback should still be there
	entry, ok := GetAgentEntry("agent")
	if !ok {
		t.Fatal("agent not found")
	}
	if entry.Model != "provider/main" {
		t.Errorf("expected model 'provider/main', got %q", entry.Model)
	}
	if len(entry.Fallback) != 1 || entry.Fallback[0] != "provider/fb1" {
		t.Errorf("expected fallback preserved, got %v", entry.Fallback)
	}
}

func TestAgentsSaveAndLoadRoundtrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create agents through the API
	err := SetAgent("agent1", "provider/main1")
	if err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	err = SetFallback("agent1", []string{"provider/fb1"})
	if err != nil {
		t.Fatalf("SetFallback: %v", err)
	}

	// Load back
	loaded, err := LoadAgents()
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}

	entry, ok := loaded["agent1"]
	if !ok {
		t.Fatal("agent1 not found after reload")
	}
	if entry.Model != "provider/main1" {
		t.Errorf("expected model 'provider/main1', got %q", entry.Model)
	}
	if len(entry.Fallback) != 1 || entry.Fallback[0] != "provider/fb1" {
		t.Errorf("expected fallback ['provider/fb1'], got %v", entry.Fallback)
	}
}

func TestAgentsLoadOldFormat(t *testing.T) {
	// Write an old-format config file and verify it loads correctly
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, GlobalConfigDir)
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	oldData := `{"old-agent": "provider/old-model", "another": "provider/another-model"}`
	err = os.WriteFile(filepath.Join(dir, GlobalConfigFile), []byte(oldData), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loaded, err := LoadAgents()
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}

	if e, ok := loaded["old-agent"]; !ok {
		t.Error("missing 'old-agent'")
	} else if e.Model != "provider/old-model" {
		t.Errorf("expected 'provider/old-model', got %q", e.Model)
	} else if e.Fallback != nil {
		t.Errorf("expected nil fallback, got %v", e.Fallback)
	}
}

func TestGetAgentBackwardCompat(t *testing.T) {
	// GetAgent should still work with the old map[string]string return
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := SetAgent("agent1", "provider/model1")
	if err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	// Also set an agent with just fallback but no model
	err = SetFallback("agent2", []string{"provider/fb1"})
	if err != nil {
		t.Fatalf("SetFallback: %v", err)
	}

	// GetAgent on agent1 should return the model
	m, ok := GetAgent("agent1")
	if !ok || m != "provider/model1" {
		t.Errorf("GetAgent('agent1') = %q, %v; want 'provider/model1', true", m, ok)
	}

	// GetAgent on agent2 (no model, only fallback) should return empty string but true (agent exists)
	m, ok = GetAgent("agent2")
	if !ok {
		t.Errorf("GetAgent('agent2') = %q, %v; want '', true (agent exists)", m, ok)
	}
	if m != "" {
		t.Errorf("GetAgent('agent2') model = %q; want ''", m)
	}
}

// --- ResolveModel tests ---

func TestResolveModel_ExplicitModel(t *testing.T) {
	setupConfigTest(t)
	m := ResolveModel("explicit/model", "")
	if m != "explicit/model" {
		t.Errorf("ResolveModel = %q, want 'explicit/model'", m)
	}
}

func TestResolveModel_NamedAgent(t *testing.T) {
	setupConfigTest(t)
	if err := SetAgent("my-agent", "agent/model"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	m := ResolveModel("", "my-agent")
	if m != "agent/model" {
		t.Errorf("ResolveModel = %q, want 'agent/model'", m)
	}
}

func TestResolveModel_DefaultAgent(t *testing.T) {
	setupConfigTest(t)
	if err := SetAgent("default", "default/model"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	m := ResolveModel("", "")
	if m != "default/model" {
		t.Errorf("ResolveModel = %q, want 'default/model'", m)
	}
}

func TestResolveModel_DefaultAgentWithNameDefault(t *testing.T) {
	setupConfigTest(t)
	if err := SetAgent("default", "default/model"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	// Passing agentName "default" should still find the default agent
	m := ResolveModel("", "default")
	if m != "default/model" {
		t.Errorf("ResolveModel = %q, want 'default/model'", m)
	}
}

func TestResolveModel_DefaultModelFromConfig(t *testing.T) {
	setupConfigTest(t)
	// Set default_model in config.json (no agents configured)
	if err := SetDefaultModel("config/default/model"); err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}
	m := ResolveModel("", "")
	if m != "config/default/model" {
		t.Errorf("ResolveModel = %q, want 'config/default/model'", m)
	}
}

func TestResolveModel_NothingConfigured(t *testing.T) {
	setupConfigTest(t)
	m := ResolveModel("", "")
	if m != "" {
		t.Errorf("ResolveModel = %q, want empty", m)
	}
}

func TestResolveModel_AgentOverridesConfigDefault(t *testing.T) {
	setupConfigTest(t)
	// Both "default" agent and config default_model set
	if err := SetAgent("default", "agent-default/model"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	if err := SetDefaultModel("config-default/model"); err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}
	// Agent should win over config
	m := ResolveModel("", "")
	if m != "agent-default/model" {
		t.Errorf("ResolveModel = %q, want 'agent-default/model' (agent should win over config)", m)
	}
}

func TestResolveModel_NamedAgentOverridesDefaultAgent(t *testing.T) {
	setupConfigTest(t)
	if err := SetAgent("default", "default/model"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	if err := SetAgent("custom", "custom/model"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	m := ResolveModel("", "custom")
	if m != "custom/model" {
		t.Errorf("ResolveModel = %q, want 'custom/model'", m)
	}
}

func TestResolveModel_ExplicitModelOverridesEverything(t *testing.T) {
	setupConfigTest(t)
	if err := SetAgent("default", "default/model"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	if err := SetDefaultModel("config/model"); err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}
	m := ResolveModel("explicit/model", "default")
	if m != "explicit/model" {
		t.Errorf("ResolveModel = %q, want 'explicit/model' (explicit trumps all)", m)
	}
}

func TestResolveModel_AgentNameDefaultNoDefaultAgentFallsToConfig(t *testing.T) {
	setupConfigTest(t)
	// No "default" agent, but config has default_model
	if err := SetDefaultModel("config/model"); err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}
	m := ResolveModel("", "default")
	if m != "config/model" {
		t.Errorf("ResolveModel = %q, want 'config/model'", m)
	}
}

func TestResolveModel_FullPriorityChain(t *testing.T) {
	setupConfigTest(t)
	// Test the full chain: --model > --agent > "default" agent > config.json
	if err := SetAgent("default", "default-agent/model"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	if err := SetAgent("named", "named-agent/model"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	if err := SetDefaultModel("config-default/model"); err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}

	tests := []struct {
		name      string
		model     string
		agentName string
		want      string
	}{
		{"explicit model wins", "explicit/model", "named", "explicit/model"},
		{"named agent wins over default agent", "", "named", "named-agent/model"},
		{"default agent wins over config", "", "", "default-agent/model"},
		{"nonexistent agent falls to default agent", "", "nonexistent", "default-agent/model"},
		{"empty everything falls to config", "", "", "default-agent/model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveModel(tt.model, tt.agentName)
			if got != tt.want {
				t.Errorf("ResolveModel(%q, %q) = %q, want %q", tt.model, tt.agentName, got, tt.want)
			}
		})
	}
}

// --- Markdown agent tests: per-project discovery on top of internal/agentdefs ---

func TestListMarkdownAgents_ProjectLocalVisible(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "reviewer", "model: anthropic/claude-opus", "You review code.")

	names, err := ListMarkdownAgents()
	if err != nil {
		t.Fatalf("ListMarkdownAgents: %v", err)
	}
	if len(names) != 1 || names[0] != "reviewer" {
		t.Errorf("ListMarkdownAgents() = %v, want [reviewer]", names)
	}
}

func TestGetMarkdownAgent_ProjectLocalVisible(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "reviewer", "model: anthropic/claude-opus", "You review code.")

	got, err := GetMarkdownAgent("reviewer")
	if err != nil {
		t.Fatalf("GetMarkdownAgent: %v", err)
	}
	if got.Name != "reviewer" {
		t.Errorf("Name = %q, want %q", got.Name, "reviewer")
	}
	if got.Frontmatter.Model != "anthropic/claude-opus" {
		t.Errorf("Model = %q, want %q", got.Frontmatter.Model, "anthropic/claude-opus")
	}
	if got.SystemPrompt != "You review code." {
		t.Errorf("SystemPrompt = %q, want %q", got.SystemPrompt, "You review code.")
	}
}

func TestGetMarkdownAgent_ProjectOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(home, GlobalConfigDir, "agents"), "coder", "model: openai/global-model", "Global coder.")
	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "coder", "model: openai/project-model", "Project coder.")

	got, err := GetMarkdownAgent("coder")
	if err != nil {
		t.Fatalf("GetMarkdownAgent: %v", err)
	}
	if got.Frontmatter.Model != "openai/project-model" {
		t.Errorf("Model = %q, want project-local model %q (project should override global)", got.Frontmatter.Model, "openai/project-model")
	}

	names, err := ListMarkdownAgents()
	if err != nil {
		t.Fatalf("ListMarkdownAgents: %v", err)
	}
	if len(names) != 1 || names[0] != "coder" {
		t.Errorf("ListMarkdownAgents() = %v, want a single deduped [coder]", names)
	}
}

func TestLoadAgents_PicksUpProjectLocalMarkdownAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "reviewer", "model: anthropic/claude-opus", "You review code.")

	agents, err := LoadAgents()
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	entry, ok := agents["reviewer"]
	if !ok {
		t.Fatal("LoadAgents() did not include project-local markdown agent 'reviewer'")
	}
	if entry.Model != "anthropic/claude-opus" {
		t.Errorf("entry.Model = %q, want %q", entry.Model, "anthropic/claude-opus")
	}
}

func TestProjectLocalDiscoveryFromRepositorySubdirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file")
	runGit("commit", "-qm", "one")

	subdir := filepath.Join(repo, "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMarkdownAgent(t, filepath.Join(repo, ".tyci", "agents"), "root-agent", "model: provider/root", "Root agent.")
	if err := os.WriteFile(filepath.Join(repo, LocalConfigFile), []byte(`{"legacy":"provider/legacy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(subdir)

	got, err := GetMarkdownAgent("root-agent")
	if err != nil {
		t.Fatalf("GetMarkdownAgent from subdirectory: %v", err)
	}
	if got.Frontmatter.Model != "provider/root" {
		t.Errorf("markdown model = %q, want provider/root", got.Frontmatter.Model)
	}

	agents, err := LoadAgents()
	if err != nil {
		t.Fatalf("LoadAgents from subdirectory: %v", err)
	}
	if got := agents["legacy"].Model; got != "provider/legacy" {
		t.Errorf("legacy config model = %q, want provider/legacy", got)
	}
	wantConfig := filepath.Join(repo, LocalConfigFile)
	gotConfig := ConfigPath()
	gotResolved, err := filepath.EvalSymlinks(gotConfig)
	if err != nil {
		t.Fatalf("EvalSymlinks(ConfigPath()): %v", err)
	}
	wantResolved, err := filepath.EvalSymlinks(wantConfig)
	if err != nil {
		t.Fatalf("EvalSymlinks(want config): %v", err)
	}
	if gotResolved != wantResolved {
		t.Errorf("ConfigPath() = %q (resolved %q), want %q (resolved %q)", gotConfig, gotResolved, wantConfig, wantResolved)
	}
}

func TestLoadAgents_JSONConfigWinsOverMarkdownAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	// A markdown agent named "reviewer" ...
	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "reviewer", "model: markdown/model", "Markdown reviewer.")
	// ... and a JSON config entry with the same name but a different model.
	if err := SetAgent("reviewer", "json/model"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}

	agents, err := LoadAgents()
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	entry, ok := agents["reviewer"]
	if !ok {
		t.Fatal("LoadAgents() missing 'reviewer'")
	}
	if entry.Model != "json/model" {
		t.Errorf("entry.Model = %q, want %q (JSON config must win over markdown agent)", entry.Model, "json/model")
	}
}

func TestMarkdownAgentFrontmatter_TemperatureSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "tempagent", "model: anthropic/claude-opus\ntemperature: 0.7", "Deterministic-ish.")

	got, err := GetMarkdownAgent("tempagent")
	if err != nil {
		t.Fatalf("GetMarkdownAgent: %v", err)
	}
	if got.Frontmatter.Temperature != 0.7 {
		t.Errorf("Frontmatter.Temperature = %v, want 0.7", got.Frontmatter.Temperature)
	}
}

func TestMarkdownAgentFrontmatter_TemperatureUnsetDefaultsToZero(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "notempagent", "model: anthropic/claude-opus", "No temperature set.")

	got, err := GetMarkdownAgent("notempagent")
	if err != nil {
		t.Fatalf("GetMarkdownAgent: %v", err)
	}
	if got.Frontmatter.Temperature != 0 {
		t.Errorf("Frontmatter.Temperature = %v, want 0 (unset unwraps to zero value)", got.Frontmatter.Temperature)
	}
}

func TestMarkdownAgentFrontmatter_ToolsJoinedFromCommaList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "toolagent", "model: anthropic/claude-opus\ntools: read, bash", "Does stuff.")

	got, err := GetMarkdownAgent("toolagent")
	if err != nil {
		t.Fatalf("GetMarkdownAgent: %v", err)
	}
	if got.Frontmatter.Tools != "read, bash" {
		t.Errorf("Frontmatter.Tools = %q, want %q", got.Frontmatter.Tools, "read, bash")
	}
}
