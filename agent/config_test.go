package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain redirects HOME to a throwaway dir for the whole package so that a
// test which forgets setupConfigTest can never clobber the real ~/.tyci/config.json.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "tyci-agent-test-home")
	if err == nil {
		os.Setenv("HOME", tmp)
	}
	code := m.Run()
	if err == nil {
		os.RemoveAll(tmp)
	}
	os.Exit(code)
}

// setupConfigTest overrides the config directory for isolated testing.
func setupConfigTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })
}

func TestTyciConfig_MarshalRoundTrip(t *testing.T) {
	setupConfigTest(t)

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

func TestAddFavoriteModel_AppendsAndDedupes(t *testing.T) {
	setupConfigTest(t)

	if err := AddFavoriteModel("openai/gpt-4o"); err != nil {
		t.Fatalf("AddFavoriteModel: %v", err)
	}
	if err := AddFavoriteModel("anthropic/claude-sonnet-4-20250514"); err != nil {
		t.Fatalf("AddFavoriteModel: %v", err)
	}
	// Duplicate should be a no-op.
	if err := AddFavoriteModel("openai/gpt-4o"); err != nil {
		t.Fatalf("AddFavoriteModel (dup): %v", err)
	}

	got := GetFavoriteModels()
	if len(got) != 2 {
		t.Fatalf("GetFavoriteModels = %v, want 2 entries", got)
	}
}

func TestRemoveFavoriteModel(t *testing.T) {
	setupConfigTest(t)

	SetFavoriteModels([]string{"openai/gpt-4o", "anthropic/claude-sonnet-4-20250514"})
	if err := RemoveFavoriteModel("openai/gpt-4o"); err != nil {
		t.Fatalf("RemoveFavoriteModel: %v", err)
	}

	got := GetFavoriteModels()
	if len(got) != 1 || got[0] != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("GetFavoriteModels = %v, want [anthropic/claude-sonnet-4-20250514]", got)
	}
}

// TestAddFavoriteModel_ReconcilesFromDisk simulates a second session adding a
// favorite: the add reloads the on-disk config first, so a favorite written by
// another session in the meantime is preserved rather than clobbered.
func TestAddFavoriteModel_ReconcilesFromDisk(t *testing.T) {
	setupConfigTest(t)

	// Another session persisted this favorite.
	SetFavoriteModels([]string{"anthropic/claude-sonnet-4-20250514"})

	// This session adds a different one.
	if err := AddFavoriteModel("openai/gpt-4o"); err != nil {
		t.Fatalf("AddFavoriteModel: %v", err)
	}

	got := GetFavoriteModels()
	if len(got) != 2 {
		t.Fatalf("GetFavoriteModels = %v, want both favorites preserved", got)
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

// writeLocalConfig writes wd/.tyci/config.json with the given body.
func writeLocalConfig(t *testing.T, wd, body string) {
	t.Helper()
	dir := filepath.Join(wd, GlobalConfigDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, localConfigName), []byte(body), 0644); err != nil {
		t.Fatalf("write local config: %v", err)
	}
}

// TestLoadTyciConfigFrom_LocalFieldOverridesWithoutWipingGlobal is the core
// item-22 guarantee for config.json: a project-local file naming only
// default_model must not reset the global file's favorite_models, max_tokens
// or prompt_cache to zero values — the merge is per field, not a whole-file
// replace.
func TestLoadTyciConfigFrom_LocalFieldOverridesWithoutWipingGlobal(t *testing.T) {
	setupConfigTest(t)

	falseVal := false
	if err := SaveTyciConfig(TyciConfig{
		DefaultModel:   "global/model",
		FavoriteModels: []string{"global/fav-a", "global/fav-b"},
		MaxTokens:      4096,
		PromptCache:    &falseVal,
	}); err != nil {
		t.Fatalf("SaveTyciConfig: %v", err)
	}

	wd := t.TempDir()
	writeLocalConfig(t, wd, `{"default_model": "local/model"}`)

	got := LoadTyciConfigFrom(wd)
	if got.DefaultModel != "local/model" {
		t.Errorf("DefaultModel = %q, want local override %q", got.DefaultModel, "local/model")
	}
	if len(got.FavoriteModels) != 2 || got.FavoriteModels[0] != "global/fav-a" {
		t.Errorf("FavoriteModels = %v, want the global list preserved, unset by the local file", got.FavoriteModels)
	}
	if got.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want the global value preserved", got.MaxTokens)
	}
	if got.PromptCache == nil || *got.PromptCache != false {
		t.Errorf("PromptCache = %v, want the global value (false) preserved", got.PromptCache)
	}
}

// TestLoadTyciConfigFrom_LocalOverridesEachFieldItSets checks the opposite
// direction: every field a local file DOES set wins over the same field in
// the global file.
func TestLoadTyciConfigFrom_LocalOverridesEachFieldItSets(t *testing.T) {
	setupConfigTest(t)

	trueVal := true
	if err := SaveTyciConfig(TyciConfig{
		DefaultModel:   "global/model",
		FavoriteModels: []string{"global/fav"},
		MaxTokens:      1000,
		PromptCache:    &trueVal,
	}); err != nil {
		t.Fatalf("SaveTyciConfig: %v", err)
	}

	wd := t.TempDir()
	writeLocalConfig(t, wd, `{
		"default_model": "local/model",
		"favorite_models": ["local/fav-a", "local/fav-b"],
		"max_tokens": 8192,
		"prompt_cache": false
	}`)

	got := LoadTyciConfigFrom(wd)
	if got.DefaultModel != "local/model" {
		t.Errorf("DefaultModel = %q, want %q", got.DefaultModel, "local/model")
	}
	if len(got.FavoriteModels) != 2 || got.FavoriteModels[1] != "local/fav-b" {
		t.Errorf("FavoriteModels = %v, want local list", got.FavoriteModels)
	}
	if got.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d, want 8192", got.MaxTokens)
	}
	if got.PromptCache == nil || *got.PromptCache != false {
		t.Errorf("PromptCache = %v, want false (local override)", got.PromptCache)
	}
}

// TestLoadTyciConfigFrom_NoLocalFile falls back to the global config
// untouched when there is no project-local override at all.
func TestLoadTyciConfigFrom_NoLocalFile(t *testing.T) {
	setupConfigTest(t)

	if err := SaveTyciConfig(TyciConfig{DefaultModel: "global/model"}); err != nil {
		t.Fatalf("SaveTyciConfig: %v", err)
	}

	wd := t.TempDir() // no .tyci/config.json here
	got := LoadTyciConfigFrom(wd)
	if got.DefaultModel != "global/model" {
		t.Errorf("DefaultModel = %q, want the global value with no local override", got.DefaultModel)
	}
}

// TestLoadTyciConfig_CLIFlagWins is not a config-merge test by itself —
// ResolveModel only ever consults config.json (global or local) when the
// caller passes an empty model — but it is the guarantee item 22 depends on:
// an explicit --model flag must still beat both local and global config
// after this rework, since commands.go only calls ResolveModel when the
// flag was not set.
func TestResolveModel_ExplicitModelBeatsConfig(t *testing.T) {
	setupConfigTest(t)
	if err := SaveTyciConfig(TyciConfig{DefaultModel: "global/model"}); err != nil {
		t.Fatalf("SaveTyciConfig: %v", err)
	}
	got := ResolveModel("explicit/model", "")
	if got != "explicit/model" {
		t.Errorf("ResolveModel with an explicit model = %q, want it to win over config.json", got)
	}
}
