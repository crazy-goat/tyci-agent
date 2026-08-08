// Package agent provides named agent configurations mapping agent names to default models.
// Config is loaded from local (.tyci.json) then global (~/.tyci/agents.json).
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentsFile is the name of the local config file (in current working directory).
const LocalConfigFile = ".tyci.json"

// MarkdownAgentsDir is the directory for markdown agent definitions.
const MarkdownAgentsDir = "agents"

// GlobalConfigDir is relative to HOME.
const GlobalConfigDir = ".tyci"

// GlobalConfigFile is the agents config file name in the global directory.
const GlobalConfigFile = "agents.json"

// AgentEntry holds the model and optional fallback models for an agent.
// Supports both old format (string value) and new format (object value) in JSON.
type AgentEntry struct {
	Model    string   `json:"model"`
	Fallback []string `json:"fallback,omitempty"`
}

// MarkdownAgentFrontmatter holds YAML frontmatter from a markdown agent file.
type MarkdownAgentFrontmatter struct {
	Model          string   `yaml:"model"`
	Tools          string   `yaml:"tools"`
	MaxIterations  int      `yaml:"max_iterations"`
	Temperature    float64  `yaml:"temperature"`
	SystemPrompt   string   `yaml:"system"`
	Description    string   `yaml:"description"`
	FallbackModels []string `yaml:"fallback"`
}

// MarkdownAgent represents a full agent definition loaded from a .md file.
type MarkdownAgent struct {
	Name         string
	Frontmatter  MarkdownAgentFrontmatter
	SystemPrompt string
	FilePath     string
}

// AgentsDirPath returns the path to the markdown agents directory.
func AgentsDirPath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "~"
	}
	return filepath.Join(home, GlobalConfigDir, MarkdownAgentsDir)
}

// MarshalJSON writes as plain string if no fallback, object otherwise.
func (e AgentEntry) MarshalJSON() ([]byte, error) {
	if len(e.Fallback) == 0 {
		return json.Marshal(e.Model)
	}
	// Use alias to avoid infinite recursion
	type alias AgentEntry
	return json.Marshal(alias(e))
}

// UnmarshalJSON accepts both "string" and {"model":"...","fallback":[...]}.
func (e *AgentEntry) UnmarshalJSON(data []byte) error {
	// Try string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		e.Model = s
		e.Fallback = nil
		return nil
	}
	// Try object
	type alias AgentEntry
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	e.Model = a.Model
	e.Fallback = a.Fallback
	return nil
}

// Agents maps agent name -> agent entry (model + optional fallback).
type Agents map[string]AgentEntry

// localConfigPath returns the local config path in the given working directory.
func localConfigPath(wd string) string {
	return filepath.Join(wd, LocalConfigFile)
}

// globalConfigPath returns the global config path under HOME.
func globalConfigPath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "~"
	}
	return filepath.Join(home, GlobalConfigDir, GlobalConfigFile)
}

// LoadAgents loads agents from local config first (if exists), then merges with global config.
// Local values override global ones. Also loads markdown agents from ~/.tyci/agents/.
func LoadAgents() (Agents, error) {
	result := make(Agents)

	// Load global first
	gPath := globalConfigPath()
	if data, err := os.ReadFile(gPath); err == nil {
		var g Agents
		if err := json.Unmarshal(data, &g); err == nil {
			for k, v := range g {
				result[k] = v
			}
		}
	}

	// Load local (overrides global)
	wd, _ := os.Getwd()
	lPath := localConfigPath(wd)
	if data, err := os.ReadFile(lPath); err == nil {
		var l Agents
		if err := json.Unmarshal(data, &l); err == nil {
			for k, v := range l {
				result[k] = v
			}
		}
	}

	// Load markdown agents (they become entries with just model set)
	mdAgents, _ := LoadMarkdownAgents(AgentsDirPath())
	for _, md := range mdAgents {
		if _, exists := result[md.Name]; !exists {
			result[md.Name] = AgentEntry{Model: md.Frontmatter.Model}
		}
	}

	return result, nil
}

// LoadMarkdownAgents reads .md files from the given directory.
// Each file must have YAML frontmatter between --- markers.
// The markdown body becomes the system prompt.
func LoadMarkdownAgents(dir string) ([]MarkdownAgent, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agents dir: %w", err)
	}

	var agents []MarkdownAgent
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		agent, err := parseMarkdownAgent(entry.Name(), data)
		if err != nil {
			continue
		}
		agent.FilePath = path
		agents = append(agents, *agent)
	}

	return agents, nil
}

// parseMarkdownAgent parses a markdown agent file with YAML frontmatter.
func parseMarkdownAgent(filename string, data []byte) (*MarkdownAgent, error) {
	content := string(data)

	// Look for YAML frontmatter between --- markers
	if !strings.HasPrefix(content, "---") {
		return nil, fmt.Errorf("no frontmatter")
	}

	parts := strings.SplitN(content[3:], "---", 2)
	if len(parts) < 2 {
		return nil, fmt.Errorf("unclosed frontmatter")
	}

	var fm MarkdownAgentFrontmatter
	if err := yaml.Unmarshal([]byte(parts[0]), &fm); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	systemPrompt := strings.TrimSpace(parts[1])
	if fm.SystemPrompt != "" {
		systemPrompt = fm.SystemPrompt
	}

	// Use filename without .md as agent name
	name := strings.TrimSuffix(filename, ".md")

	return &MarkdownAgent{
		Name:         name,
		Frontmatter:  fm,
		SystemPrompt: systemPrompt,
	}, nil
}

// GetMarkdownAgent returns a markdown agent by name.
func GetMarkdownAgent(name string) (*MarkdownAgent, error) {
	agents, err := LoadMarkdownAgents(AgentsDirPath())
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		if a.Name == name {
			return &a, nil
		}
	}
	return nil, fmt.Errorf("markdown agent %q not found", name)
}

// ListMarkdownAgents returns names of all markdown agents.
func ListMarkdownAgents() ([]string, error) {
	agents, err := LoadMarkdownAgents(AgentsDirPath())
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	return names, nil
}

// SaveGlobal saves agents to the global config file.
func SaveGlobal(a Agents) error {
	path := globalConfigPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal agents: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// SaveLocal saves agents to the local config file in the given working directory.
func SaveLocal(a Agents, wd string) error {
	path := localConfigPath(wd)
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal agents: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// saveToActiveConfig saves agents to the active config (local if exists, otherwise global).
func saveToActiveConfig(a Agents) error {
	wd, _ := os.Getwd()
	lPath := localConfigPath(wd)
	if _, err := os.Stat(lPath); err == nil {
		return SaveLocal(a, wd)
	}
	return SaveGlobal(a)
}

// SetAgent sets an agent name to a model string and saves to the appropriate location.
// If a local config exists, saves locally; otherwise saves globally.
// Preserves any existing fallback configuration for the agent.
func SetAgent(name, model string) error {
	if name == "" {
		return fmt.Errorf("agent name is required")
	}
	if model == "" {
		return fmt.Errorf("model is required (format: provider/model)")
	}

	agents, _ := LoadAgents()
	if agents == nil {
		agents = make(Agents)
	}
	entry, ok := agents[name]
	if !ok {
		entry = AgentEntry{}
	}
	entry.Model = model
	agents[name] = entry

	return saveToActiveConfig(agents)
}

// GetAgent returns the model for a given agent name.
func GetAgent(name string) (string, bool) {
	agents, err := LoadAgents()
	if err != nil || agents == nil {
		return "", false
	}
	v, ok := agents[name]
	if !ok {
		return "", false
	}
	return v.Model, true
}

// GetAgentEntry returns the full AgentEntry for a given agent name.
func GetAgentEntry(name string) (AgentEntry, bool) {
	agents, err := LoadAgents()
	if err != nil || agents == nil {
		return AgentEntry{}, false
	}
	v, ok := agents[name]
	return v, ok
}

// GetFallbackModels returns the fallback models for a given agent name.
// Returns nil if agent has no fallback configured.
func GetFallbackModels(name string) []string {
	agents, err := LoadAgents()
	if err != nil || agents == nil {
		return nil
	}
	v, ok := agents[name]
	if !ok {
		return nil
	}
	return v.Fallback
}

// SetFallback sets the fallback models for an agent and saves config.
// If fallbacks is empty, removes fallback configuration.
func SetFallback(name string, fallbacks []string) error {
	if name == "" {
		return fmt.Errorf("agent name is required")
	}

	agents, _ := LoadAgents()
	if agents == nil {
		agents = make(Agents)
	}

	entry, ok := agents[name]
	if !ok {
		// Agent doesn't exist yet — create it with empty model
		entry = AgentEntry{}
	}

	entry.Fallback = fallbacks
	agents[name] = entry

	return saveToActiveConfig(agents)
}

// DeleteAgent removes an agent by name. "default" cannot be deleted.
func DeleteAgent(name string) error {
	if name == "default" {
		return fmt.Errorf("cannot delete default agent")
	}
	if name == "" {
		return fmt.Errorf("agent name is required")
	}

	agents, err := LoadAgents()
	if err != nil || agents == nil {
		return fmt.Errorf("agent %q not found", name)
	}
	if _, ok := agents[name]; !ok {
		return fmt.Errorf("agent %q not found", name)
	}

	delete(agents, name)

	return saveToActiveConfig(agents)
}

// ListAgents returns a sorted list of agent names.
func ListAgents() ([]string, error) {
	agents, err := LoadAgents()
	if err != nil || agents == nil {
		return nil, err
	}
	names := make([]string, 0, len(agents))
	for k := range agents {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

// AllAgents returns the full agents map.
func AllAgents() (Agents, error) {
	return LoadAgents()
}

// ResolveModel resolves the effective model:
// - If model is explicitly provided, use it
// - Otherwise look up agentName in config (fallback to "default")
// - Otherwise fall back to default_model from config.json
// - Returns empty string if nothing found
func ResolveModel(model string, agentName string) string {
	if model != "" {
		return model
	}

	// Try the named agent first
	if agentName != "" {
		if m, ok := GetAgent(agentName); ok && m != "" {
			return m
		}
	}

	// Fall back to "default" agent
	if agentName != "default" {
		if m, ok := GetAgent("default"); ok && m != "" {
			return m
		}
	}

	// Fall back to default_model from config.json
	if m := GetDefaultModel(); m != "" {
		return m
	}

	return ""
}

// ConfigPath returns the active config path for display purposes.
func ConfigPath() string {
	wd, _ := os.Getwd()
	lPath := localConfigPath(wd)
	if _, err := os.Stat(lPath); err == nil {
		return lPath
	}
	return globalConfigPath()
}

// DisplayAgents pretty-prints all agents.
func DisplayAgents() error {
	agents, err := LoadAgents()
	if err != nil {
		return err
	}
	if agents == nil || len(agents) == 0 {
		fmt.Println("No agents configured.")
		return nil
	}

	names := make([]string, 0, len(agents))
	for k := range agents {
		names = append(names, k)
	}
	sort.Strings(names)

	path := ConfigPath()
	fmt.Fprintf(os.Stderr, "Config: %s\n\n", path)
	fmt.Println("Agents:")
	for _, name := range names {
		entry := agents[name]
		modelStr := entry.Model
		if len(entry.Fallback) > 0 {
			modelStr += fmt.Sprintf("  (fallback: %s)", strings.Join(entry.Fallback, ", "))
		}
		fmt.Printf("  %-20s  %s\n", name, modelStr)
	}
	return nil
}

// IsLocalConfig returns true if a local config file exists.
func IsLocalConfig() bool {
	wd, _ := os.Getwd()
	_, err := os.Stat(localConfigPath(wd))
	return err == nil
}

// SetLocal sets an agent in the local config (creates if needed).
func SetLocal(name, model string) error {
	if name == "" {
		return fmt.Errorf("agent name is required")
	}
	if model == "" {
		return fmt.Errorf("model is required (format: provider/model)")
	}

	wd, _ := os.Getwd()
	lPath := localConfigPath(wd)

	agents := make(Agents)
	if data, err := os.ReadFile(lPath); err == nil {
		json.Unmarshal(data, &agents)
	}
	if agents == nil {
		agents = make(Agents)
	}
	entry, ok := agents[name]
	if !ok {
		entry = AgentEntry{}
	}
	entry.Model = model
	agents[name] = entry
	return SaveLocal(agents, wd)
}

// DeleteLocal removes an agent from local config. "default" cannot be deleted.
func DeleteLocal(name string) error {
	if name == "default" {
		return fmt.Errorf("cannot delete default agent")
	}
	if name == "" {
		return fmt.Errorf("agent name is required")
	}

	wd, _ := os.Getwd()
	lPath := localConfigPath(wd)

	agents := make(Agents)
	if data, err := os.ReadFile(lPath); err != nil {
		return fmt.Errorf("local config not found: %w", err)
	} else {
		if err := json.Unmarshal(data, &agents); err != nil {
			return fmt.Errorf("parse local config: %w", err)
		}
	}
	if _, ok := agents[name]; !ok {
		return fmt.Errorf("agent %q not found in local config", name)
	}
	delete(agents, name)
	return SaveLocal(agents, wd)
}

// parseModelFlag extracts provider and model from "provider/model" format.
func parseModelFlag(s string) (provider, model string, err error) {
	if s == "" {
		return "", "", fmt.Errorf("empty model")
	}
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid model format: %q (expected provider/model)", s)
	}
	return parts[0], parts[1], nil
}
