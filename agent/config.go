// Package agent provides named agent configurations.
// This file manages tyci's config.json: global (~/.tyci/config.json) merged
// with an optional project-local override (<wd>/.tyci/config.json).
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

const globalConfigName = "config.json"

// localConfigName is the project-local override of config.json, read from
// <wd>/.tyci/config.json — same directory (and the same trust posture: this
// file carries no command and no code, only data, so unlike hooks.json and
// .tyci/tools it is not trust-gated) that already holds hooks.json and
// agents/.
const localConfigName = "config.json"

// TyciConfig holds global tyci settings stored in ~/.tyci/config.json.
type TyciConfig struct {
	DefaultModel   string   `json:"default_model,omitempty"`
	FavoriteModels []string `json:"favorite_models,omitempty"`
	// MaxTokens caps the model's reply length. 0 (or absent) leaves it to the
	// connector, which for Anthropic means a conservative default that is
	// safe on every model but short for the current ones. Set it once here to
	// the ceiling of the models you actually use; the --max-tokens flag
	// overrides it for a single run.
	MaxTokens int `json:"max_tokens,omitempty"`
	// PromptCache turns provider-side prompt caching on or off. A pointer so
	// that an absent key means "on" — caching is what keeps an agent from
	// paying for the whole conversation on every turn, so it has to be the
	// default rather than something to opt into. Set it to false only for an
	// endpoint that rejects the cache_control field.
	PromptCache *bool `json:"prompt_cache,omitempty"`
	// SidebarVisible persists the TUI's right-side sidebar (Ctrl+T) across
	// restarts. Absent key means false (closed): the sidebar only appears on
	// startup once the user has opened and kept it, which is when the TUI
	// saves true.
	SidebarVisible bool `json:"sidebar_visible,omitempty"`
	// AutoCompactPercent is the legacy hard limit as a percentage of the
	// model's context window. 0/absent means automatic (95%); a negative
	// value disables auto-compaction. compact_hard_limit wins over it.
	AutoCompactPercent int `json:"auto_compact_percent,omitempty"`
	// CompactSoftLimit and CompactHardLimit are the default soft and hard
	// context limits in tokens (0/absent = computed from the model window).
	// An agent (agents.json, agent definition) or a flow role may override
	// them.
	CompactSoftLimit int `json:"compact_soft_limit,omitempty"`
	CompactHardLimit int `json:"compact_hard_limit,omitempty"`
	// FirstByteTimeoutSec is how long a provider has to start answering a
	// request; StreamIdleTimeoutSec is the silence allowed in a running
	// stream. 0 or absent means 30. On expiry the request is retried.
	FirstByteTimeoutSec  int `json:"first_byte_timeout_sec,omitempty"`
	StreamIdleTimeoutSec int `json:"stream_idle_timeout_sec,omitempty"`
	// Watchdog tunes idle detection of running subagents. Global config only:
	// a project-local value is ignored (see mergeTyciConfig).
	Watchdog *WatchdogConfig `json:"watchdog,omitempty"`
	// PingInterval is a Go duration string, for example "5m": every agent
	// posts a progress note at least this often (the model is nudged at half
	// of it, the harness posts "[auto]" at the full interval). Global config
	// only: a project-local value is ignored (see mergeTyciConfig).
	PingInterval string `json:"ping_interval,omitempty"`
}

// DefaultPingInterval is the ping interval when ping_interval is not set.
const DefaultPingInterval = 5 * time.Minute

// PingIntervalDuration returns ping_interval, default 5m. An unparsable or
// non-positive value is an error naming the key.
func (c TyciConfig) PingIntervalDuration() (time.Duration, error) {
	if c.PingInterval == "" {
		return DefaultPingInterval, nil
	}
	d, err := time.ParseDuration(c.PingInterval)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("ping_interval: %q is not a positive duration (example: \"5m\")", c.PingInterval)
	}
	return d, nil
}

// WatchdogConfig holds Go duration strings, for example "3m".
type WatchdogConfig struct {
	IdleAfter     string `json:"idle_after,omitempty"`
	EscalateAfter string `json:"escalate_after,omitempty"`
}

// WatchdogDurations returns the idle and escalation delays. Missing values
// default to 3m. An unparsable or non-positive value is an error naming the key.
func (c TyciConfig) WatchdogDurations() (idle, escalate time.Duration, err error) {
	idle, escalate = 3*time.Minute, 3*time.Minute
	if c.Watchdog == nil {
		return idle, escalate, nil
	}
	for _, f := range []struct {
		key string
		in  string
		out *time.Duration
	}{
		{"watchdog.idle_after", c.Watchdog.IdleAfter, &idle},
		{"watchdog.escalate_after", c.Watchdog.EscalateAfter, &escalate},
	} {
		if f.in == "" {
			continue
		}
		d, perr := time.ParseDuration(f.in)
		if perr != nil || d <= 0 {
			return 0, 0, fmt.Errorf("%s: %q is not a positive duration (example: \"3m\")", f.key, f.in)
		}
		*f.out = d
	}
	return idle, escalate, nil
}

// globalConfigDir returns the path to ~/.tyci.
func globalConfigDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "~"
	}
	return filepath.Join(home, GlobalConfigDir)
}

// globalConfigFilePath returns ~/.tyci/config.json.
func globalConfigFilePath() string {
	return filepath.Join(globalConfigDir(), globalConfigName)
}

// localConfigFilePath returns <wd>/.tyci/config.json.
func localConfigFilePath(wd string) string {
	return filepath.Join(wd, GlobalConfigDir, localConfigName)
}

// readTyciConfigFile reads one config.json file. A missing file (or one that
// fails to parse) yields the zero value rather than an error: both hold the
// meaning "nothing configured here" to the merge below.
func readTyciConfigFile(path string) TyciConfig {
	var cfg TyciConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

// mergeTyciConfig combines a global and a local config field by field: a
// local value wins for the fields it sets, and every field the local file
// leaves unset falls back to the global value. This is deliberately NOT a
// whole-file replace — a local config.json naming only default_model must
// not wipe the global file's favorite_models, max_tokens or prompt_cache.
func mergeTyciConfig(global, local TyciConfig) TyciConfig {
	merged := global
	if local.DefaultModel != "" {
		merged.DefaultModel = local.DefaultModel
	}
	if local.FavoriteModels != nil {
		merged.FavoriteModels = local.FavoriteModels
	}
	if local.MaxTokens != 0 {
		merged.MaxTokens = local.MaxTokens
	}
	if local.PromptCache != nil {
		merged.PromptCache = local.PromptCache
	}
	if local.SidebarVisible {
		merged.SidebarVisible = true
	}
	if local.AutoCompactPercent != 0 {
		merged.AutoCompactPercent = local.AutoCompactPercent
	}
	if local.CompactSoftLimit != 0 {
		merged.CompactSoftLimit = local.CompactSoftLimit
	}
	if local.CompactHardLimit != 0 {
		merged.CompactHardLimit = local.CompactHardLimit
	}
	if local.FirstByteTimeoutSec != 0 {
		merged.FirstByteTimeoutSec = local.FirstByteTimeoutSec
	}
	if local.StreamIdleTimeoutSec != 0 {
		merged.StreamIdleTimeoutSec = local.StreamIdleTimeoutSec
	}
	return merged
}

// LoadTyciConfigFrom loads ~/.tyci/config.json merged with <wd>/.tyci/config.json
// (see mergeTyciConfig for the per-field precedence). Exported mainly for
// testing with an explicit wd; production callers use LoadTyciConfig.
func LoadTyciConfigFrom(wd string) TyciConfig {
	global := readTyciConfigFile(globalConfigFilePath())
	if wd == "" {
		return global
	}
	local := readTyciConfigFile(localConfigFilePath(wd))
	return mergeTyciConfig(global, local)
}

// LoadTyciConfig loads ~/.tyci/config.json merged with the current working
// directory's project-local <wd>/.tyci/config.json, if any. Returns the
// global-only config when cwd cannot be determined or carries no override.
func LoadTyciConfig() TyciConfig {
	wd, _ := os.Getwd()
	return LoadTyciConfigFrom(wd)
}

// SaveTyciConfig writes the config to ~/.tyci/config.json.
func SaveTyciConfig(cfg TyciConfig) error {
	dir := globalConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// Keep keys this struct does not know (models, roles, orchestrator, forge).
	rest := map[string]json.RawMessage{}
	if old, err := os.ReadFile(globalConfigFilePath()); err == nil {
		if json.Unmarshal(old, &rest) != nil || rest == nil {
			rest = map[string]json.RawMessage{}
		}
	}
	t := reflect.TypeOf(cfg)
	for i := 0; i < t.NumField(); i++ {
		delete(rest, strings.Split(t.Field(i).Tag.Get("json"), ",")[0])
	}
	own, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(own, &rest); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(globalConfigFilePath(), data, 0644)
}

// GetDefaultModel returns the default model from config.
func GetDefaultModel() string {
	return LoadTyciConfig().DefaultModel
}

// SetDefaultModel saves the default model to config.
func SetDefaultModel(model string) error {
	cfg := LoadTyciConfig()
	cfg.DefaultModel = model
	return SaveTyciConfig(cfg)
}

// GetFavoriteModels returns the list of favorite models from config.
func GetFavoriteModels() []string {
	return LoadTyciConfig().FavoriteModels
}

// SetFavoriteModels saves the list of favorite models to config.
func SetFavoriteModels(models []string) error {
	cfg := LoadTyciConfig()
	cfg.FavoriteModels = models
	return SaveTyciConfig(cfg)
}

// AddFavoriteModel adds a single model to the favorites, reloading the config
// first so concurrent tyci sessions don't clobber each other's favorites.
// No-op if the model is already a favorite.
func AddFavoriteModel(model string) error {
	cfg := LoadTyciConfig()
	for _, f := range cfg.FavoriteModels {
		if f == model {
			return nil
		}
	}
	cfg.FavoriteModels = append(cfg.FavoriteModels, model)
	return SaveTyciConfig(cfg)
}

// RemoveFavoriteModel removes a single model from the favorites, reloading the
// config first so concurrent sessions only ever conflict on the same model.
func RemoveFavoriteModel(model string) error {
	cfg := LoadTyciConfig()
	out := cfg.FavoriteModels[:0]
	for _, f := range cfg.FavoriteModels {
		if f != model {
			out = append(out, f)
		}
	}
	cfg.FavoriteModels = out
	return SaveTyciConfig(cfg)
}

// GetMaxTokens returns the configured reply cap, or 0 when unset.
func GetMaxTokens() int {
	return LoadTyciConfig().MaxTokens
}

// CompactLimits returns the soft and hard context limits for the named agent:
// its agents.json entry first, then the global config. 0 means unset.
func CompactLimits(agentName string) (soft, hard int) {
	cfg := LoadTyciConfig()
	soft, hard = cfg.CompactSoftLimit, cfg.CompactHardLimit
	if e, ok := GetAgentEntry(agentName); ok {
		if e.CompactSoftLimit > 0 {
			soft = e.CompactSoftLimit
		}
		if e.CompactHardLimit > 0 {
			hard = e.CompactHardLimit
		}
	}
	return soft, hard
}

// GetAutoCompactPercent returns the configured legacy auto-compact
// percentage, or 0 when unset (automatic limits apply).
func GetAutoCompactPercent() int {
	return LoadTyciConfig().AutoCompactPercent
}

// promptCacheOnce memoises the config read behind PromptCacheEnabled. Every
// subagent builds its own agent.Config, and re-reading a small JSON file per
// child would be pointless work for a value that cannot change mid-process.
var (
	promptCacheOnce sync.Once
	promptCacheOn   bool
)

// PromptCacheEnabled reports whether requests may carry cache_control.
// Absent config means yes.
func PromptCacheEnabled() bool {
	promptCacheOnce.Do(func() {
		cfg := LoadTyciConfig()
		promptCacheOn = cfg.PromptCache == nil || *cfg.PromptCache
	})
	return promptCacheOn
}
