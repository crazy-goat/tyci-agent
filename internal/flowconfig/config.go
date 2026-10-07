// Package flowconfig loads the workflow config (models, default_model, roles)
// from ~/.tyci/config.json and, for trusted projects, .tyci/config.json.
package flowconfig

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const defaultCheckTimeout = 1800 * time.Second

// Config is the merged workflow config.
type Config struct {
	Models          map[string]string `json:"models"`                 // alias -> provider URI
	DefaultModel    string            `json:"default_model"`          // alias or provider/model name, used when a role has no model
	Roles           map[string]Role   `json:"roles"`                  //
	CheckTimeoutSec int               `json:"check_timeout_sec"`      // 0 means 1800
	Forge           Forge             `json:"forge"`                  // where the orchestrator reads issues
	Orchestrator    json.RawMessage   `json:"orchestrator,omitempty"` // read by orchestrator.LoadConfig

	// Keys owned by the agent config (agent.TyciConfig) in the same file.
	// They are accepted so the file loads, and ignored here.
	FavoriteModels     json.RawMessage `json:"favorite_models,omitempty"`
	MaxTokens          json.RawMessage `json:"max_tokens,omitempty"`
	PromptCache        json.RawMessage `json:"prompt_cache,omitempty"`
	SidebarVisible     json.RawMessage `json:"sidebar_visible,omitempty"`
	AutoCompactPercent json.RawMessage `json:"auto_compact_percent,omitempty"`
}

// Forge selects the forge of the orchestrator.
type Forge struct {
	Kind string `json:"kind"` // "" or "github"
	Repo string `json:"repo"` // "owner/name"; "" or "auto" reads the git remote
}

var forgeRepoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Role holds the model alias or provider/model name and system prompt of one workflow role.
type Role struct {
	Model  string `json:"model"`  // alias or provider/model name; empty -> DefaultModel
	Prompt string `json:"prompt"` // system prompt; "@file.md" = file relative to the config file
}

//go:embed prompts/*.md
var promptFS embed.FS

// defaultPrompt returns the embedded prompt of a builtin role.
func defaultPrompt(role string) (string, bool) {
	if !isBuiltinRole(role) {
		return "", false
	}
	b, err := promptFS.ReadFile("prompts/" + role + ".md")
	if err != nil {
		return "", false
	}
	return string(b), true
}

func isBuiltinRole(name string) bool {
	return name == "worker" || name == "review" || name == "merge_decision" || name == "oracle"
}

// Load reads the global file and, only when trusted, the project file, then merges and validates them.
func Load(home, projectDir string, trusted bool) (*Config, error) {
	merged, err := readFile(filepath.Join(home, ".tyci", "config.json"))
	if err != nil {
		return nil, err
	}
	if trusted {
		proj, err := readFile(filepath.Join(projectDir, ".tyci", "config.json"))
		if err != nil {
			return nil, err
		}
		for k, v := range proj.Models {
			merged.Models[k] = v
		}
		for k, v := range proj.Roles {
			merged.Roles[k] = v
		}
		if proj.DefaultModel != "" {
			merged.DefaultModel = proj.DefaultModel
		}
		if proj.Forge.Kind != "" {
			merged.Forge.Kind = proj.Forge.Kind
		}
		if proj.Forge.Repo != "" {
			merged.Forge.Repo = proj.Forge.Repo
		}
		if proj.CheckTimeoutSec != 0 {
			merged.CheckTimeoutSec = proj.CheckTimeoutSec
		}
	}
	if err := merged.validate(); err != nil {
		return nil, err
	}
	return merged, nil
}

func readFile(path string) (*Config, error) {
	c := &Config{Models: map[string]string{}, Roles: map[string]Role{}}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Models == nil {
		c.Models = map[string]string{}
	}
	if c.Roles == nil {
		c.Roles = map[string]Role{}
	}
	dir := filepath.Dir(path)
	for name, r := range c.Roles {
		if rel, ok := strings.CutPrefix(r.Prompt, "@"); ok {
			text, err := readPromptFile(dir, rel)
			if err != nil {
				return nil, fmt.Errorf("%s: role %q: %w", path, name, err)
			}
			r.Prompt = text
			c.Roles[name] = r
		}
	}
	return c, nil
}

func readPromptFile(dir, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("prompt file %q must be relative", rel)
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == ".." {
			return "", fmt.Errorf("prompt file %q must not contain \"..\"", rel)
		}
	}
	full := filepath.Join(dir, rel)
	info, err := os.Lstat(full)
	if err != nil {
		return "", fmt.Errorf("prompt file %q (%s): %w", rel, full, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("prompt file %q (%s) is a symlink", rel, full)
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("prompt file %q (%s): %w", rel, full, err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return "", fmt.Errorf("prompt file %q (%s): prompt file is empty", rel, full)
	}
	return string(b), nil
}

func (c *Config) validate() error {
	if k := c.Forge.Kind; k != "" && k != "github" {
		return fmt.Errorf("forge.kind: unknown kind %q (only \"github\" is supported)", k)
	}
	if r := c.Forge.Repo; r != "" && r != "auto" && !forgeRepoRe.MatchString(r) {
		return fmt.Errorf("forge.repo: %q is not \"auto\" or \"owner/name\"", r)
	}
	if c.DefaultModel != "" && !c.hasModel(c.DefaultModel) {
		return fmt.Errorf("default_model: unknown model alias %q", c.DefaultModel)
	}
	for name, r := range c.Roles {
		if r.Model != "" && !c.hasModel(r.Model) {
			return fmt.Errorf("role %q: unknown model alias %q", name, r.Model)
		}
	}
	return nil
}

func (c *Config) hasModel(model string) bool {
	if _, ok := c.Models[model]; ok {
		return true
	}
	return strings.Contains(model, "/")
}

// Role returns the named role. Only worker, review, merge_decision and oracle fall back to embedded prompts.
func (c *Config) Role(name string) (Role, error) {
	r, ok := c.Roles[name]
	if ok && r.Prompt != "" {
		if name == "oracle" && r.Model == "" {
			r.Model = "opus"
		}
		return r, nil
	}
	if p, found := defaultPrompt(name); found {
		r.Prompt = p
		if name == "oracle" && r.Model == "" {
			r.Model = "opus"
		}
		return r, nil
	}
	if ok {
		return r, nil
	}
	return Role{}, fmt.Errorf("role %q is not defined", name)
}

// ResolveModel returns the provider URI for the role.
func (c *Config) ResolveModel(r Role) (string, error) {
	alias := r.Model
	if alias == "" {
		alias = c.DefaultModel
	}
	if alias == "" {
		return "", errors.New("no model set for the role and no default_model")
	}
	if uri, ok := c.Models[alias]; ok {
		return uri, nil
	}
	if strings.Contains(alias, "/") {
		return alias, nil
	}
	return "", fmt.Errorf("unknown model alias %q", alias)
}

// CheckTimeout returns the CI check timeout (30 minutes by default).
func (c *Config) CheckTimeout() time.Duration {
	if c.CheckTimeoutSec == 0 {
		return defaultCheckTimeout
	}
	return time.Duration(c.CheckTimeoutSec) * time.Second
}
