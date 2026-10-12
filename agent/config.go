// Package agent provides tyci's agent configuration.
// This file manages tyci's config.json: global (~/.tyci/config.json) merged
// with an optional project-local override (<wd>/.tyci/config.json).
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/gitinfo"
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
	DefaultModel string `json:"default_model,omitempty"`
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
	// A flow role may override them.
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
	// Tools tunes when a tool call moves to the background. Nil means the
	// built-in defaults. Global and local files merge per key (see
	// mergeToolsConfig).
	Tools *ToolsConfig `json:"tools,omitempty"`
}

// ToolsConfig is the "tools" block of config.json. The pointer fields
// distinguish an absent key from 0: 0 means "background at once".
type ToolsConfig struct {
	// BackgroundAfterFastSec is how long a fast tool waits before it moves to
	// the background. Absent means defaultBackgroundAfterFast.
	BackgroundAfterFastSec *float64 `json:"background_after_fast_sec,omitempty"`
	// BackgroundAfterSlowSec is the same for a slow tool. Absent means
	// defaultBackgroundAfterSlow.
	BackgroundAfterSlowSec *float64 `json:"background_after_slow_sec,omitempty"`
	// BackgroundAfterSec overrides the wait of one tool by name. It wins over
	// the class value.
	BackgroundAfterSec map[string]float64 `json:"background_after_sec,omitempty"`
	// ResultTailBytes is the size limit of a late result in the conversation.
	// Absent means DefaultResultTailBytes.
	ResultTailBytes *int `json:"result_tail_bytes,omitempty"`
}

const (
	// defaultBackgroundAfterFast is the wait of a fast tool before it is backgrounded.
	defaultBackgroundAfterFast = 2500 * time.Millisecond
	// defaultBackgroundAfterSlow is the wait of a slow tool before it is backgrounded.
	defaultBackgroundAfterSlow = 60 * time.Second
	// DefaultResultTailBytes is the result_tail_bytes default.
	DefaultResultTailBytes = 16384
	// maxBackgroundAfterSec keeps a configured wait inside time.Duration (about 285 years).
	maxBackgroundAfterSec = 9_000_000_000
)

// Validate reports the first invalid value. The error names its key.
func (c ToolsConfig) Validate() error {
	if c.BackgroundAfterFastSec != nil {
		if err := checkBackgroundAfterSec("tools.background_after_fast_sec", *c.BackgroundAfterFastSec); err != nil {
			return err
		}
	}
	if c.BackgroundAfterSlowSec != nil {
		if err := checkBackgroundAfterSec("tools.background_after_slow_sec", *c.BackgroundAfterSlowSec); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(c.BackgroundAfterSec))
	for name := range c.BackgroundAfterSec {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		key := "tools.background_after_sec." + name
		if name == "subagent" {
			return fmt.Errorf("%s: subagent always blocks, so it has no background_after value; remove this key", key)
		}
		if err := checkBackgroundAfterSec(key, c.BackgroundAfterSec[name]); err != nil {
			return err
		}
	}
	if c.ResultTailBytes != nil && *c.ResultTailBytes < 0 {
		return fmt.Errorf("tools.result_tail_bytes: %d is negative; use 0 or more bytes", *c.ResultTailBytes)
	}
	return nil
}

// checkBackgroundAfterSec reports an error when sec is not a usable number of seconds.
func checkBackgroundAfterSec(key string, sec float64) error {
	if sec < 0 || sec > maxBackgroundAfterSec {
		return fmt.Errorf("%s: %v is not a number of seconds from 0 to %d", key, sec, maxBackgroundAfterSec)
	}
	return nil
}

// ResultTailLimit returns result_tail_bytes, default DefaultResultTailBytes.
func (c ToolsConfig) ResultTailLimit() int {
	if c.ResultTailBytes == nil {
		return DefaultResultTailBytes
	}
	return *c.ResultTailBytes
}

// ToolsSettings returns the tools block, or the zero value when it is absent.
func (c TyciConfig) ToolsSettings() ToolsConfig {
	if c.Tools == nil {
		return ToolsConfig{}
	}
	return *c.Tools
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
	return gitinfo.ProjectPath(wd, localConfigName)
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
// not wipe the global file's max_tokens or prompt_cache.
func mergeTyciConfig(global, local TyciConfig) TyciConfig {
	merged := global
	if local.DefaultModel != "" {
		merged.DefaultModel = local.DefaultModel
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
	merged.Tools = mergeToolsConfig(global.Tools, local.Tools)
	return merged
}

// mergeToolsConfig merges the tools blocks field by field. A local field that
// is set (non-nil, so 0 counts) wins. A local BackgroundAfterSec map adds to
// the global map: a key set in both files takes the local value. The result is
// a new value, so neither input is changed.
func mergeToolsConfig(global, local *ToolsConfig) *ToolsConfig {
	if local == nil {
		return global
	}
	if global == nil {
		return local
	}
	merged := *global
	if local.BackgroundAfterFastSec != nil {
		merged.BackgroundAfterFastSec = local.BackgroundAfterFastSec
	}
	if local.BackgroundAfterSlowSec != nil {
		merged.BackgroundAfterSlowSec = local.BackgroundAfterSlowSec
	}
	if local.ResultTailBytes != nil {
		merged.ResultTailBytes = local.ResultTailBytes
	}
	if local.BackgroundAfterSec != nil {
		merged.BackgroundAfterSec = make(map[string]float64, len(global.BackgroundAfterSec)+len(local.BackgroundAfterSec))
		for name, sec := range global.BackgroundAfterSec {
			merged.BackgroundAfterSec[name] = sec
		}
		for name, sec := range local.BackgroundAfterSec {
			merged.BackgroundAfterSec[name] = sec
		}
	}
	return &merged
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

// GetMaxTokens returns the configured reply cap, or 0 when unset.
func GetMaxTokens() int {
	return LoadTyciConfig().MaxTokens
}

// CompactLimits returns the global soft and hard context limits in tokens.
// 0 means unset.
func CompactLimits() (soft, hard int) {
	cfg := LoadTyciConfig()
	return cfg.CompactSoftLimit, cfg.CompactHardLimit
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
