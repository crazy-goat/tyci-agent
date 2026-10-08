// Package agent provides tyci's agent configuration and the markdown agent
// definitions. The config itself lives in config.go.
package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/crazy-goat/tyci-agent/internal/agentdefs"
)

// MarkdownAgentsDir is the directory for markdown agent definitions.
const MarkdownAgentsDir = "agents"

// GlobalConfigDir is relative to HOME.
const GlobalConfigDir = ".tyci"

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

// AgentsDirPath returns the path to the global markdown agents directory
// (used for display purposes; agent discovery itself also looks at the
// project-local directory via internal/agentdefs).
func AgentsDirPath() string {
	return agentdefs.GlobalDir()
}

// markdownAgentFromDef converts an internal/agentdefs.Def (the single
// parser for markdown agent files) into the public MarkdownAgent facade
// type, so callers outside this package keep seeing the same shape.
func markdownAgentFromDef(def agentdefs.Def) MarkdownAgent {
	// def.Temperature is *float64 (nil means "unset" in agentdefs), but the
	// public MarkdownAgentFrontmatter.Temperature stays a plain float64 for
	// backward compatibility with existing callers of this package. Unwrap
	// the pointer here, at the facade boundary: nil becomes 0.0, which is
	// indistinguishable from an explicit "temperature: 0" at this level.
	var temperature float64
	if def.Temperature != nil {
		temperature = *def.Temperature
	}

	return MarkdownAgent{
		Name: def.Name,
		Frontmatter: MarkdownAgentFrontmatter{
			Model:          def.Model,
			Tools:          strings.Join(def.Tools, ", "),
			MaxIterations:  def.MaxIterations,
			Temperature:    temperature,
			SystemPrompt:   def.SystemPrompt,
			Description:    def.Description,
			FallbackModels: def.Fallback,
		},
		SystemPrompt: def.SystemPrompt,
		FilePath:     def.Path,
	}
}

// LoadMarkdownAgents reads .md files from the given directory.
// Each file must have YAML frontmatter between --- markers.
// The markdown body becomes the system prompt.
// This is a thin, single-directory wrapper around internal/agentdefs, which
// is the single place in the repo that actually parses these files.
func LoadMarkdownAgents(dir string) ([]MarkdownAgent, error) {
	defs, err := agentdefs.LoadDir(dir)
	if err != nil {
		return nil, err
	}
	agents := make([]MarkdownAgent, 0, len(defs))
	for _, def := range defs {
		agents = append(agents, markdownAgentFromDef(def))
	}
	return agents, nil
}

// GetMarkdownAgent returns a markdown agent by name, looking in both the
// global (~/.tyci/agents) and project-local (<project-root>/.tyci/agents)
// directories. The project-local definition wins on name collisions.
func GetMarkdownAgent(name string) (*MarkdownAgent, error) {
	def, ok := agentdefs.Get("", name)
	if !ok {
		return nil, fmt.Errorf("markdown agent %q not found", name)
	}
	agent := markdownAgentFromDef(def)
	return &agent, nil
}

// ListMarkdownAgents returns names of all markdown agents visible from the
// current working directory: global (~/.tyci/agents) plus project-local
// (<project-root>/.tyci/agents), merged with project-local taking precedence.
func ListMarkdownAgents() ([]string, error) {
	defs := agentdefs.List("")
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	sort.Strings(names)
	return names, nil
}
