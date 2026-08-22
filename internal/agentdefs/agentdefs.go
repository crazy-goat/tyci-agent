// Package agentdefs is the single place in the repo that parses and
// discovers agent definitions from markdown files. It has no dependency on
// any other tyci package (stdlib + gopkg.in/yaml.v3 only) so it can be
// imported freely without pulling in unrelated code.
package agentdefs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Def is a fully parsed markdown agent definition.
type Def struct {
	Name          string   // filename without .md
	Description   string   // frontmatter `description`
	Model         string   // frontmatter `model` (e.g. "anthropic/claude-sonnet-5")
	Tools         []string // frontmatter `tools`, comma-separated, trimmed; nil = no restriction
	MaxIterations int      // frontmatter `max_iterations`; 0 = unset
	// Temperature is nil when the frontmatter omits it. A pointer, not a
	// plain float64, because 0 is a meaningful value ("deterministic")
	// and must be distinguishable from "unset".
	Temperature *float64
	// MaxTokens is frontmatter `max_tokens`; 0 = unset, meaning the
	// connector's own default applies. Useful on an agent whose whole job is
	// to produce something long (a report, a generated file), where the
	// conservative default would truncate the answer mid-sentence.
	MaxTokens    int
	Fallback     []string // frontmatter `fallback`
	SystemPrompt string   // markdown body, or frontmatter `system` if set (overrides body)
	// SystemPromptMode is "append" (default) or "replace". In append mode the
	// definition's body is a ROLE layered on top of the standard subagent
	// system prompt, so the agent keeps the subagent contract, environment
	// context, tool descriptions and the project's AGENTS.md. In replace mode
	// the body IS the entire system prompt — full control, and full
	// responsibility for restating anything it needs.
	SystemPromptMode string
	Path             string // absolute path to the .md file
}

// frontmatter holds the raw YAML frontmatter fields of a markdown agent file.
// Temperature is a pointer (not a plain float64) so that yaml.v3 can
// distinguish an omitted `temperature` key (nil) from an explicit
// `temperature: 0` (pointer to 0.0).
type frontmatter struct {
	Model            string   `yaml:"model"`
	Tools            string   `yaml:"tools"`
	MaxIterations    int      `yaml:"max_iterations"`
	Temperature      *float64 `yaml:"temperature"`
	MaxTokens        int      `yaml:"max_tokens"`
	System           string   `yaml:"system"`
	Description      string   `yaml:"description"`
	Fallback         []string `yaml:"fallback"`
	SystemPromptMode string   `yaml:"system_prompt_mode"`
}

// minTemperature and maxTemperature bound the closed interval [0, 2] that
// Parse accepts for `temperature`. The upper bound of 2 is the widest window
// among supported providers (OpenAI and Gemini both accept 0..2; Anthropic
// accepts only 0..1). agentdefs deliberately does not enforce the narrower
// Anthropic range here because it has no idea which provider an agent will
// ultimately run on — that provider's own server enforces its stricter limit.
const (
	minTemperature = 0.0
	maxTemperature = 2.0
)

// SystemPromptModeAppend and SystemPromptModeReplace are the two values
// Def.SystemPromptMode can hold. Append is the default: it layers the
// definition's body as a role on top of the standard subagent system
// prompt (contract, environment context, AGENTS.md, skills) instead of
// discarding all of that. Replace preserves the pre-existing behavior of
// treating the body as the entire system prompt.
const (
	SystemPromptModeAppend  = "append"
	SystemPromptModeReplace = "replace"
)

// GlobalDir returns the global agent definitions directory: $HOME/.tyci/agents.
func GlobalDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "~"
	}
	return filepath.Join(home, ".tyci", "agents")
}

// ProjectDir returns the project-local agent definitions directory:
// <wd>/.tyci/agents. If wd is empty, the current working directory is used.
func ProjectDir(wd string) string {
	if wd == "" {
		wd, _ = os.Getwd()
	}
	return filepath.Join(wd, ".tyci", "agents")
}

// Dirs returns the ordered list of directories to load agent definitions
// from: global first, then project-local. Later directories win when merged
// by Load.
func Dirs(wd string) []string {
	return []string{GlobalDir(), ProjectDir(wd)}
}

// Parse parses a single markdown agent file's contents. filename is used to
// derive Name (without .md suffix); it is not read from disk here.
func Parse(filename string, data []byte) (Def, error) {
	content := string(data)

	if !strings.HasPrefix(content, "---") {
		return Def{}, fmt.Errorf("no frontmatter")
	}

	parts := strings.SplitN(content[3:], "---", 2)
	if len(parts) < 2 {
		return Def{}, fmt.Errorf("unclosed frontmatter")
	}

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(parts[0]), &fm); err != nil {
		return Def{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	if fm.Temperature != nil && (*fm.Temperature < minTemperature || *fm.Temperature > maxTemperature) {
		return Def{}, fmt.Errorf("temperature %v out of range [%v,%v]", *fm.Temperature, minTemperature, maxTemperature)
	}

	// A negative cap is a typo, and 0 already means "unset" — so only a
	// negative number is rejected. No upper bound is enforced: the ceiling is
	// per-model and only the provider knows it, and a 400 naming the real
	// limit is more useful than a guess made here.
	if fm.MaxTokens < 0 {
		return Def{}, fmt.Errorf("max_tokens %d must not be negative", fm.MaxTokens)
	}

	// Normalize the empty/omitted case to "append" right here so every
	// consumer (providers, tools, main) can compare against a concrete
	// string instead of also handling "". Anything other than the two known
	// values is a typo, not a silent fallback — same posture as the
	// temperature range check above.
	systemPromptMode := fm.SystemPromptMode
	if systemPromptMode == "" {
		systemPromptMode = SystemPromptModeAppend
	}
	if systemPromptMode != SystemPromptModeAppend && systemPromptMode != SystemPromptModeReplace {
		return Def{}, fmt.Errorf("system_prompt_mode %q must be %q or %q", systemPromptMode, SystemPromptModeAppend, SystemPromptModeReplace)
	}

	systemPrompt := strings.TrimSpace(parts[1])
	if fm.System != "" {
		systemPrompt = fm.System
	}

	var tools []string
	for _, t := range strings.Split(fm.Tools, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tools = append(tools, t)
		}
	}

	return Def{
		Name:             strings.TrimSuffix(filename, ".md"),
		Description:      fm.Description,
		Model:            fm.Model,
		Tools:            tools,
		MaxIterations:    fm.MaxIterations,
		Temperature:      fm.Temperature,
		MaxTokens:        fm.MaxTokens,
		Fallback:         fm.Fallback,
		SystemPrompt:     systemPrompt,
		SystemPromptMode: systemPromptMode,
	}, nil
}

// LoadDir reads all .md files in dir and parses them as agent definitions.
// A missing directory returns (nil, nil). Files that fail to read or parse
// are silently skipped so a single broken file does not block the rest.
func LoadDir(dir string) ([]Def, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agents dir: %w", err)
	}

	var defs []Def
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		def, err := Parse(entry.Name(), data)
		if err != nil {
			continue
		}
		def.Path = path
		defs = append(defs, def)
	}

	return defs, nil
}

// Load merges agent definitions from dirs, in order. If the same Name
// appears in multiple directories, the definition from the later directory
// wins. The result is sorted ascending by Name.
func Load(dirs []string) []Def {
	byName := make(map[string]Def)
	for _, dir := range dirs {
		defs, err := LoadDir(dir)
		if err != nil {
			continue
		}
		for _, def := range defs {
			byName[def.Name] = def
		}
	}

	result := make([]Def, 0, len(byName))
	for _, def := range byName {
		result = append(result, def)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// List returns all agent definitions visible from wd: global then
// project-local, merged with project-local taking precedence.
func List(wd string) []Def {
	return Load(Dirs(wd))
}

// Get returns the agent definition named name, visible from wd.
func Get(wd, name string) (Def, bool) {
	for _, def := range List(wd) {
		if def.Name == name {
			return def, true
		}
	}
	return Def{}, false
}
