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
	"slices"
	"strings"
	"time"
)

const defaultCheckTimeout = 1800 * time.Second

// Config is the merged workflow config.
type Config struct {
	Models          map[string]string `json:"models"`                 // alias -> provider URI
	DefaultModel    string            `json:"default_model"`          // alias or provider/model name, used when a role has no model
	DefaultEffort   string            `json:"default_effort"`         // reasoning effort, used when a role has no effort
	Roles           map[string]Role   `json:"roles"`                  //
	CheckTimeoutSec int               `json:"check_timeout_sec"`      // 0 means 1800
	Forge           Forge             `json:"forge"`                  // where the orchestrator reads issues
	Orchestrator    json.RawMessage   `json:"orchestrator,omitempty"` // read by orchestrator.LoadConfig
	Logs            Logs              `json:"logs"`                   // run transcripts

	// Keys owned by the agent config (agent.TyciConfig) in the same file.
	// They are accepted so the file loads, and ignored here. favorite_models
	// was removed in v0.6.0; it stays accepted so an old file still loads.
	LegacyFavorites    json.RawMessage `json:"favorite_models,omitempty"`
	MaxTokens          json.RawMessage `json:"max_tokens,omitempty"`
	PromptCache        json.RawMessage `json:"prompt_cache,omitempty"`
	SidebarVisible     json.RawMessage `json:"sidebar_visible,omitempty"`
	AutoCompactPercent json.RawMessage `json:"auto_compact_percent,omitempty"`
	CompactSoftLimit   json.RawMessage `json:"compact_soft_limit,omitempty"`
	CompactHardLimit   json.RawMessage `json:"compact_hard_limit,omitempty"`

	FirstByteTimeoutSec  json.RawMessage `json:"first_byte_timeout_sec,omitempty"`
	StreamIdleTimeoutSec json.RawMessage `json:"stream_idle_timeout_sec,omitempty"`
	Watchdog             json.RawMessage `json:"watchdog,omitempty"`
	PingInterval         json.RawMessage `json:"ping_interval,omitempty"`
}

// Logs configures the run transcripts under ~/.tyci/runs.
type Logs struct {
	// RetentionDays: finished runs older than this are deleted. Nil means 30, 0 keeps all.
	RetentionDays *int `json:"retention_days,omitempty"`
}

// DefaultRetentionDays is the retention when logs.retention_days is not set.
const DefaultRetentionDays = 30

// RetentionDays returns logs.retention_days or the default.
func (c *Config) RetentionDays() int {
	if c.Logs.RetentionDays == nil {
		return DefaultRetentionDays
	}
	return *c.Logs.RetentionDays
}

// Forge selects the forge of the orchestrator.
type Forge struct {
	Kind string `json:"kind"` // "" or "github"
	Repo string `json:"repo"` // "owner/name"; "" or "auto" reads the git remote
}

var forgeRepoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Role holds the model alias or provider/model name and system prompt of one workflow role.
type Role struct {
	Model  string `json:"model"`            // alias or provider/model name; empty -> DefaultModel
	Prompt string `json:"prompt"`           // system prompt; "@file.md" = file relative to the config file
	Effort string `json:"effort,omitempty"` // reasoning effort; empty -> DefaultEffort
	// Context limits in tokens for this role; 0 = use the global defaults.
	CompactSoftLimit int `json:"compact_soft_limit,omitempty"`
	CompactHardLimit int `json:"compact_hard_limit,omitempty"`
	// Fallback lists model aliases or provider/model names tried in order
	// when the model of the role fails.
	Fallback []string `json:"fallback,omitempty"`
	// Permission is read-only, worktree-write or full. Empty means worktree-write.
	Permission string `json:"permission,omitempty"`
	// Network is nil (true) or false. False removes web and MCP tools.
	Network *bool `json:"network,omitempty"`
	// Tools lists the tools of the role. Nil means the default set of the permission.
	Tools []string `json:"tools,omitempty"`
	// Limits stop the run of the role. Zero means no limit (see Limits).
	Limits Limits `json:"limits,omitempty"`
}

// Limits are the run limits of one role. Zero means unlimited, except PingMin.
type Limits struct {
	Rounds    int `json:"rounds,omitempty"`     // model turns
	TimeMin   int `json:"time_min,omitempty"`   // minutes of wall time
	ToolCalls int `json:"tool_calls,omitempty"` // tool calls
	PingMin   int `json:"ping_min,omitempty"`   // minutes between progress notes
}

// Permission modes of a role, from the most restricted to the least.
const (
	PermissionReadOnly      = "read-only"
	PermissionWorktreeWrite = "worktree-write"
	PermissionFull          = "full"
)

// permissions are the accepted permission modes, from the most restricted.
var permissions = []string{PermissionReadOnly, PermissionWorktreeWrite, PermissionFull}

// PermissionMode returns the permission of the role. Empty means worktree-write.
func (r Role) PermissionMode() string {
	if r.Permission == "" {
		return PermissionWorktreeWrite
	}
	return r.Permission
}

// NetworkOn reports whether the role may use the network (web and MCP tools).
func (r Role) NetworkOn() bool {
	return r.Network == nil || *r.Network
}

//go:embed prompts/*.md
var promptFS embed.FS

// DefaultPrompt returns the embedded prompt of a builtin role.
func DefaultPrompt(role string) (string, bool) {
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
	return name == "worker" || name == "review" || name == "fixer" || name == "oracle"
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
			merged.Roles[k] = tighter(merged.Roles[k], v)
		}
		if proj.DefaultModel != "" {
			merged.DefaultModel = proj.DefaultModel
		}
		if proj.DefaultEffort != "" {
			merged.DefaultEffort = proj.DefaultEffort
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
			text, err := ReadPromptFile(dir, rel)
			if err != nil {
				return nil, fmt.Errorf("%s: role %q: %w", path, name, err)
			}
			r.Prompt = text
			c.Roles[name] = r
		}
	}
	return c, nil
}

// ReadPromptFile reads the prompt file rel, relative to dir. rel must be
// relative, without "..", not a symlink and not empty.
func ReadPromptFile(dir, rel string) (string, error) {
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

// tighter returns the local role of the repository file, limited by the global
// role of the same name. A role that only the repository file defines has the
// zero global role, so its permission is capped at worktree-write.
// Model, prompt, effort, fallback and limits come from local.
// A key that local omits is inherited from global.
// Permission and network can only get stricter. Tools can only be narrowed,
// and a local list cannot add an MCP tool that global does not list.
func tighter(global, local Role) Role {
	out := local
	ceiling := PermissionWorktreeWrite
	if global.Permission != "" {
		ceiling = global.Permission
	}
	switch {
	case local.Permission == "":
		out.Permission = global.Permission
	case permissionRank(local.Permission) > permissionRank(ceiling):
		out.Permission = ceiling
	}
	if local.Network == nil {
		out.Network = global.Network
	} else if !global.NetworkOn() {
		off := false
		out.Network = &off
	}
	switch {
	case local.Tools == nil:
		out.Tools = global.Tools
	case global.Tools == nil:
		out.Tools = withoutMCP(local.Tools)
	default:
		out.Tools = intersectTools(global.Tools, local.Tools)
	}
	return out
}

// permissionRank orders the permission modes. Unknown modes rank as full.
func permissionRank(p string) int {
	if i := slices.Index(permissions, p); i >= 0 {
		return i
	}
	return len(permissions)
}

// withoutMCP returns names without the MCP tools (mcp_*).
func withoutMCP(names []string) []string {
	out := []string{}
	for _, n := range names {
		if !strings.HasPrefix(n, "mcp_") {
			out = append(out, n)
		}
	}
	return out
}

// intersectTools returns the names of local that global lists. A global
// entry ending in "*" lists every name with that prefix.
func intersectTools(global, local []string) []string {
	out := []string{}
	for _, n := range local {
		for _, g := range global {
			if g == n || (strings.HasSuffix(g, "*") && strings.HasPrefix(n, strings.TrimSuffix(g, "*"))) {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

func (c *Config) validate() error {
	if d := c.Logs.RetentionDays; d != nil && *d < 0 {
		return fmt.Errorf("logs.retention_days: %d is negative", *d)
	}
	if k := c.Forge.Kind; k != "" && k != "github" {
		return fmt.Errorf("forge.kind: unknown kind %q (only \"github\" is supported)", k)
	}
	if r := c.Forge.Repo; r != "" && r != "auto" && !forgeRepoRe.MatchString(r) {
		return fmt.Errorf("forge.repo: %q is not \"auto\" or \"owner/name\"", r)
	}
	if c.DefaultModel != "" && !c.hasModel(c.DefaultModel) {
		return fmt.Errorf("default_model: unknown model alias %q", c.DefaultModel)
	}
	if err := checkEffort("default_effort", c.DefaultEffort); err != nil {
		return err
	}
	for name, r := range c.Roles {
		if err := checkEffort(fmt.Sprintf("roles.%s.effort", name), r.Effort); err != nil {
			return err
		}
		if r.Model != "" && !c.hasModel(r.Model) {
			return fmt.Errorf("role %q: unknown model alias %q", name, r.Model)
		}
		if err := r.validatePolicy(name, c); err != nil {
			return err
		}
	}
	return nil
}

// validatePolicy checks the permission, fallback and limit keys of one role.
func (r Role) validatePolicy(name string, c *Config) error {
	if r.Permission != "" && !slices.Contains(permissions, r.Permission) {
		return fmt.Errorf("roles.%s.permission: unknown permission %q (use one of %s)", name, r.Permission, strings.Join(permissions, ", "))
	}
	for _, fb := range r.Fallback {
		if !c.hasModel(fb) {
			return fmt.Errorf("role %q: unknown fallback model alias %q", name, fb)
		}
	}
	limits := map[string]int{"rounds": r.Limits.Rounds, "time_min": r.Limits.TimeMin, "tool_calls": r.Limits.ToolCalls, "ping_min": r.Limits.PingMin}
	for _, key := range []string{"rounds", "time_min", "tool_calls", "ping_min"} {
		if limits[key] < 0 {
			return fmt.Errorf("roles.%s.limits.%s: %d is negative", name, key, limits[key])
		}
	}
	return nil
}

// efforts are the accepted reasoning effort levels.
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

func checkEffort(key, v string) error {
	if v == "" || slices.Contains(efforts, v) {
		return nil
	}
	return fmt.Errorf("%s: unknown effort %q (use one of %s)", key, v, strings.Join(efforts, ", "))
}

// ResolveEffort returns the effort of the role, else default_effort, else "".
func (c *Config) ResolveEffort(r Role) string {
	if r.Effort != "" {
		return r.Effort
	}
	return c.DefaultEffort
}

func (c *Config) hasModel(model string) bool {
	if _, ok := c.Models[model]; ok {
		return true
	}
	return strings.Contains(model, "/")
}

// Role returns the named role. Only worker, review, fixer and oracle fall back to embedded prompts.
func (c *Config) Role(name string) (Role, error) {
	r, ok := c.Roles[name]
	if ok && r.Prompt != "" {
		return r, nil
	}
	if p, found := DefaultPrompt(name); found {
		r.Prompt = p
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
	return c.resolveAlias(alias)
}

// ResolveFallbacks returns the provider URIs of the role's fallback aliases,
// in the order the role lists them. A role without fallbacks returns nil.
func (c *Config) ResolveFallbacks(r Role) ([]string, error) {
	var out []string
	for _, alias := range r.Fallback {
		uri, err := c.resolveAlias(alias)
		if err != nil {
			return nil, fmt.Errorf("fallback: %w", err)
		}
		out = append(out, uri)
	}
	return out, nil
}

// resolveAlias maps a model alias or a provider URI to a provider URI.
func (c *Config) resolveAlias(alias string) (string, error) {
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
