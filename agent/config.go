// Package agent provides named agent configurations.
// This file manages the global tyci config (~/.tyci/config.json).
package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const globalConfigName = "config.json"

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

// LoadTyciConfig loads ~/.tyci/config.json. Returns empty config on missing file.
func LoadTyciConfig() TyciConfig {
	var cfg TyciConfig
	data, err := os.ReadFile(globalConfigFilePath())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

// SaveTyciConfig writes the config to ~/.tyci/config.json.
func SaveTyciConfig(cfg TyciConfig) error {
	dir := globalConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
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
