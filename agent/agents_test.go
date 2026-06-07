package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
