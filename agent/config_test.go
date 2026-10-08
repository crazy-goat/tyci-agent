package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	cfg := TyciConfig{DefaultModel: "openai/gpt-4o"}
	if err := SaveTyciConfig(cfg); err != nil {
		t.Fatalf("SaveTyciConfig: %v", err)
	}

	// Re-read from the same path
	loaded := LoadTyciConfig()
	if loaded.DefaultModel != cfg.DefaultModel {
		t.Fatalf("DefaultModel = %q, want %q", loaded.DefaultModel, cfg.DefaultModel)
	}
}

func TestLoadTyciConfig_MissingFile(t *testing.T) {
	setupConfigTest(t)

	cfg := LoadTyciConfig()
	if cfg.DefaultModel != "" {
		t.Fatalf("DefaultModel = %q, want empty for missing file", cfg.DefaultModel)
	}
}

func TestSaveAndLoad_DefaultModel(t *testing.T) {
	setupConfigTest(t)

	if err := SaveTyciConfig(TyciConfig{DefaultModel: "anthropic/claude-sonnet-4-20250514"}); err != nil {
		t.Fatalf("SaveTyciConfig: %v", err)
	}

	got := GetDefaultModel()
	if got != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("GetDefaultModel = %q, want anthropic/claude-sonnet-4-20250514", got)
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
// default_model must not reset the global file's max_tokens or prompt_cache
// to zero values — the merge is per field, not a whole-file
// replace.
func TestLoadTyciConfigFrom_LocalFieldOverridesWithoutWipingGlobal(t *testing.T) {
	setupConfigTest(t)

	falseVal := false
	if err := SaveTyciConfig(TyciConfig{
		DefaultModel: "global/model",
		MaxTokens:    4096,
		PromptCache:  &falseVal,
	}); err != nil {
		t.Fatalf("SaveTyciConfig: %v", err)
	}

	wd := t.TempDir()
	writeLocalConfig(t, wd, `{"default_model": "local/model"}`)

	got := LoadTyciConfigFrom(wd)
	if got.DefaultModel != "local/model" {
		t.Errorf("DefaultModel = %q, want local override %q", got.DefaultModel, "local/model")
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
		DefaultModel: "global/model",
		MaxTokens:    1000,
		PromptCache:  &trueVal,
	}); err != nil {
		t.Fatalf("SaveTyciConfig: %v", err)
	}

	wd := t.TempDir()
	writeLocalConfig(t, wd, `{
		"default_model": "local/model",
		"max_tokens": 8192,
		"prompt_cache": false
	}`)

	got := LoadTyciConfigFrom(wd)
	if got.DefaultModel != "local/model" {
		t.Errorf("DefaultModel = %q, want %q", got.DefaultModel, "local/model")
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

func TestSaveTyciConfig_KeepsUnknownKeys(t *testing.T) {
	setupConfigTest(t)
	if err := os.MkdirAll(globalConfigDir(), 0755); err != nil {
		t.Fatal(err)
	}
	in := `{"models":{"a":"b"},"roles":{"x":1},"sidebar_visible":true,"default_model":"m"}`
	if err := os.WriteFile(globalConfigFilePath(), []byte(in), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadTyciConfig()
	cfg.SidebarVisible = false
	cfg.DefaultModel = "n"
	if err := SaveTyciConfig(cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(globalConfigFilePath())
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	var m, r bytes.Buffer
	_ = json.Compact(&m, got["models"])
	_ = json.Compact(&r, got["roles"])
	if m.String() != `{"a":"b"}` || r.String() != `{"x":1}` {
		t.Errorf("unknown keys lost: %s", data)
	}
	if _, ok := got["sidebar_visible"]; ok {
		t.Errorf("cleared key kept: %s", data)
	}
	if string(got["default_model"]) != `"n"` {
		t.Errorf("default_model not saved: %s", data)
	}
}

func TestCompactLimits_GlobalValues(t *testing.T) {
	setupConfigTest(t)
	if err := SaveTyciConfig(TyciConfig{CompactSoftLimit: 100000, CompactHardLimit: 150000}); err != nil {
		t.Fatal(err)
	}
	if soft, hard := CompactLimits(); soft != 100000 || hard != 150000 {
		t.Fatalf("global = %d, %d", soft, hard)
	}
	// The new keys survive a save that does not touch them (#298).
	cfg := LoadTyciConfig()
	cfg.DefaultModel = "m"
	if err := SaveTyciConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got := LoadTyciConfig(); got.CompactSoftLimit != 100000 || got.CompactHardLimit != 150000 {
		t.Fatalf("after save: %+v", got)
	}
}

// TestFavoritesConfigRemoved: a config.json that still has favorite_models
// loads, the other keys are read, and the removed key is ignored.
func TestFavoritesConfigRemoved(t *testing.T) {
	setupConfigTest(t)
	if err := os.MkdirAll(globalConfigDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"favorite_models":["a/b"],"default_model":"x/y","max_tokens":8000}`
	if err := os.WriteFile(globalConfigFilePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadTyciConfig()
	if cfg.DefaultModel != "x/y" || cfg.MaxTokens != 8000 {
		t.Fatalf("cfg = %+v, want default_model and max_tokens read", cfg)
	}
}

func TestTimeoutKeys_SaveAndMerge(t *testing.T) {
	setupConfigTest(t)

	if err := SaveTyciConfig(TyciConfig{FirstByteTimeoutSec: 10, StreamIdleTimeoutSec: 20}); err != nil {
		t.Fatal(err)
	}
	got := LoadTyciConfig()
	got.DefaultModel = "x/y"
	if err := SaveTyciConfig(got); err != nil {
		t.Fatal(err)
	}
	got = LoadTyciConfig()
	if got.FirstByteTimeoutSec != 10 || got.StreamIdleTimeoutSec != 20 {
		t.Fatalf("timeouts lost on save: %+v", got)
	}
	m := mergeTyciConfig(got, TyciConfig{FirstByteTimeoutSec: 5})
	if m.FirstByteTimeoutSec != 5 || m.StreamIdleTimeoutSec != 20 {
		t.Fatalf("bad merge: %+v", m)
	}
}

func TestConfig_WatchdogDefaults(t *testing.T) {
	idle, esc, err := TyciConfig{}.WatchdogDurations()
	if err != nil || idle != 3*time.Minute || esc != 3*time.Minute {
		t.Fatalf("got %v %v %v", idle, esc, err)
	}
	idle, esc, err = TyciConfig{Watchdog: &WatchdogConfig{IdleAfter: "20s"}}.WatchdogDurations()
	if err != nil || idle != 20*time.Second || esc != 3*time.Minute {
		t.Fatalf("got %v %v %v", idle, esc, err)
	}
}

func TestConfig_WatchdogInvalid(t *testing.T) {
	for _, c := range []struct {
		cfg WatchdogConfig
		key string
	}{
		{WatchdogConfig{IdleAfter: "abc"}, "watchdog.idle_after"},
		{WatchdogConfig{IdleAfter: "0s"}, "watchdog.idle_after"},
		{WatchdogConfig{EscalateAfter: "-1m"}, "watchdog.escalate_after"},
	} {
		_, _, err := TyciConfig{Watchdog: &c.cfg}.WatchdogDurations()
		if err == nil || !strings.Contains(err.Error(), c.key) {
			t.Fatalf("%+v: got %v", c.cfg, err)
		}
	}
}

func TestPingIntervalConfig(t *testing.T) {
	d, err := TyciConfig{}.PingIntervalDuration()
	if err != nil || d != 5*time.Minute {
		t.Fatalf("default: got %v %v", d, err)
	}
	d, err = TyciConfig{PingInterval: "30s"}.PingIntervalDuration()
	if err != nil || d != 30*time.Second {
		t.Fatalf("30s: got %v %v", d, err)
	}
	for _, bad := range []string{"abc", "0s", "-1m"} {
		_, err := TyciConfig{PingInterval: bad}.PingIntervalDuration()
		if err == nil || !strings.Contains(err.Error(), "ping_interval") {
			t.Fatalf("%q: expected error naming ping_interval, got %v", bad, err)
		}
	}
}

func TestPingIntervalIgnoredInProjectFile(t *testing.T) {
	merged := mergeTyciConfig(TyciConfig{PingInterval: "10m"}, TyciConfig{PingInterval: "1s"})
	if merged.PingInterval != "10m" {
		t.Fatalf("project value leaked: %q", merged.PingInterval)
	}
}
