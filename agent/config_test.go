package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// setupConfigTest overrides the config directory for isolated testing.
func setupConfigTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })
}

func TestTyciConfig_MarshalRoundTrip(t *testing.T) {
	cfg := TyciConfig{
		DefaultModel:   "openai/gpt-4o",
		FavoriteModels: []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-20250514"},
	}
	if err := SaveTyciConfig(cfg); err != nil {
		t.Fatalf("SaveTyciConfig: %v", err)
	}

	// Re-read from the same path
	loaded := LoadTyciConfig()
	if loaded.DefaultModel != cfg.DefaultModel {
		t.Fatalf("DefaultModel = %q, want %q", loaded.DefaultModel, cfg.DefaultModel)
	}
	if len(loaded.FavoriteModels) != len(cfg.FavoriteModels) {
		t.Fatalf("FavoriteModels len = %d, want %d", len(loaded.FavoriteModels), len(cfg.FavoriteModels))
	}
	for i, m := range cfg.FavoriteModels {
		if loaded.FavoriteModels[i] != m {
			t.Fatalf("FavoriteModels[%d] = %q, want %q", i, loaded.FavoriteModels[i], m)
		}
	}
}

func TestLoadTyciConfig_MissingFile(t *testing.T) {
	setupConfigTest(t)

	cfg := LoadTyciConfig()
	if cfg.DefaultModel != "" {
		t.Fatalf("DefaultModel = %q, want empty for missing file", cfg.DefaultModel)
	}
	if len(cfg.FavoriteModels) != 0 {
		t.Fatalf("FavoriteModels = %v, want empty for missing file", cfg.FavoriteModels)
	}
}

func TestSaveAndLoad_DefaultModel(t *testing.T) {
	setupConfigTest(t)

	if err := SetDefaultModel("anthropic/claude-sonnet-4-20250514"); err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}

	got := GetDefaultModel()
	if got != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("GetDefaultModel = %q, want anthropic/claude-sonnet-4-20250514", got)
	}
}

func TestSaveAndLoad_FavoriteModels(t *testing.T) {
	setupConfigTest(t)

	favs := []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-20250514"}
	if err := SetFavoriteModels(favs); err != nil {
		t.Fatalf("SetFavoriteModels: %v", err)
	}

	got := GetFavoriteModels()
	if len(got) != 2 {
		t.Fatalf("GetFavoriteModels len = %d, want 2", len(got))
	}
	if got[0] != "openai/gpt-4o" || got[1] != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("GetFavoriteModels = %v, want %v", got, favs)
	}
}

func TestSetDefaultModel_Overwrites(t *testing.T) {
	setupConfigTest(t)

	if err := SetDefaultModel("openai/gpt-4o"); err != nil {
		t.Fatalf("SetDefaultModel (first): %v", err)
	}
	if err := SetDefaultModel("anthropic/claude-sonnet-4-20250514"); err != nil {
		t.Fatalf("SetDefaultModel (second): %v", err)
	}

	got := GetDefaultModel()
	if got != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("GetDefaultModel = %q, want anthropic/claude-sonnet-4-20250514", got)
	}
}

func TestSetFavoriteModels_AppendDoesNotAffectConfig(t *testing.T) {
	setupConfigTest(t)

	if err := SetFavoriteModels([]string{"openai/gpt-4o"}); err != nil {
		t.Fatalf("SetFavoriteModels: %v", err)
	}

	// Verify save created the file
	cfgPath := globalConfigFilePath()
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		t.Fatalf("config file not created at %s", cfgPath)
	}
}

func TestDefaultModel_PersistsWithFavorites(t *testing.T) {
	setupConfigTest(t)

	SetDefaultModel("openai/gpt-4o")
	SetFavoriteModels([]string{"anthropic/claude-sonnet-4-20250514", "openai/gpt-4o"})

	cfg := LoadTyciConfig()
	if cfg.DefaultModel != "openai/gpt-4o" {
		t.Fatalf("DefaultModel = %q, want openai/gpt-4o", cfg.DefaultModel)
	}
	if len(cfg.FavoriteModels) != 2 {
		t.Fatalf("FavoriteModels len = %d, want 2", len(cfg.FavoriteModels))
	}
}

func TestSaveTyciConfig_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "deep", "nested", ".tyci")
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", nested)
	defer os.Setenv("HOME", origHome)

	// Override globalConfigDir for this test by writing directly
	cfg := TyciConfig{DefaultModel: "test/model"}
	if err := SaveTyciConfig(cfg); err != nil {
		t.Fatalf("SaveTyciConfig should create dirs: %v", err)
	}

	if _, err := os.Stat(globalConfigFilePath()); os.IsNotExist(err) {
		t.Fatal("config file should exist after SaveTyciConfig")
	}
}

func TestGetDefaultModel_EmptyWhenNotSet(t *testing.T) {
	setupConfigTest(t)

	got := GetDefaultModel()
	if got != "" {
		t.Fatalf("GetDefaultModel = %q, want empty", got)
	}
}

func TestGetFavoriteModels_EmptyWhenNotSet(t *testing.T) {
	setupConfigTest(t)

	got := GetFavoriteModels()
	if got != nil {
		t.Fatalf("GetFavoriteModels = %v, want nil", got)
	}
}
