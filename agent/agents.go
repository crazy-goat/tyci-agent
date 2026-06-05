// Package agent provides named agent configurations mapping agent names to default models.
// Config is loaded from local (.tyci-agent.json) then global (~/.tyci/agents.json).
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentsFile is the name of the local config file (in current working directory).
const LocalConfigFile = ".tyci-agent.json"

// GlobalConfigDir is relative to HOME.
const GlobalConfigDir = ".tyci"

// GlobalConfigFile is the agents config file name in the global directory.
const GlobalConfigFile = "agents.json"

// Agents maps agent name -> model string (e.g. "openai/gpt-4o").
type Agents map[string]string

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
// Local values override global ones.
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

	return result, nil
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

// SetAgent sets an agent name to a model string and saves to the appropriate location.
// If a local config exists, saves locally; otherwise saves globally.
func SetAgent(name, model string) error {
	if name == "" {
		return fmt.Errorf("agent name is required")
	}
	if model == "" {
		return fmt.Errorf("model is required (format: provider/model)")
	}

	wd, _ := os.Getwd()
	lPath := localConfigPath(wd)

	agents, _ := LoadAgents()
	if agents == nil {
		agents = make(Agents)
	}
	agents[name] = model

	// If local config exists, save locally; otherwise globally
	if _, err := os.Stat(lPath); err == nil {
		return SaveLocal(agents, wd)
	}
	return SaveGlobal(agents)
}

// GetAgent returns the model for a given agent name.
func GetAgent(name string) (string, bool) {
	agents, err := LoadAgents()
	if err != nil || agents == nil {
		return "", false
	}
	v, ok := agents[name]
	return v, ok
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

	// Save back
	wd, _ := os.Getwd()
	lPath := localConfigPath(wd)
	if _, err := os.Stat(lPath); err == nil {
		return SaveLocal(agents, wd)
	}
	return SaveGlobal(agents)
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

	// Fall back to "default"
	if agentName != "default" {
		if m, ok := GetAgent("default"); ok && m != "" {
			return m
		}
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
		fmt.Printf("  %-20s  %s\n", name, agents[name])
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
	agents[name] = model
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
