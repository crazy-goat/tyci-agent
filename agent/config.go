// Package agent provides named agent configurations.
// This file manages the global tyci config (~/.tyci/config.json).
package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const globalConfigName = "config.json"

// TyciConfig holds global tyci settings stored in ~/.tyci/config.json.
type TyciConfig struct {
	DefaultModel   string   `json:"default_model,omitempty"`
	FavoriteModels []string `json:"favorite_models,omitempty"`
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
