package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/gitinfo"
	"github.com/crazy-goat/tyci-agent/internal/hooks"
)

// setupReloadProject points HOME and the project at fresh temp dirs and
// records the project setup, as initCommon does. The hooks registry is reset
// when the test ends.
func setupReloadProject(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	wd := t.TempDir()
	rememberProjectSetup(wd, true, false)
	t.Cleanup(func() {
		hooks.Set(nil)
		projectSetup.mu.Lock()
		projectSetup.set = false
		projectSetup.mu.Unlock()
	})
	if err := os.MkdirAll(filepath.Join(wd, ".tyci"), 0o755); err != nil {
		t.Fatal(err)
	}
	return wd
}

func writeProjectHooks(t *testing.T, wd, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wd, ".tyci", "hooks.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	preHooksJSON  = `{"hooks":[{"event":"pre_tool","command":"true"}]}`
	postHooksJSON = `{"hooks":[{"event":"post_tool","command":"true"}]}`
)

func TestReloadProjectSettingsReplacesHooks(t *testing.T) {
	wd := setupReloadProject(t)
	writeProjectHooks(t, wd, preHooksJSON)
	if _, err := reloadProjectSettings(context.Background()); err != nil {
		t.Fatalf("first reload: %v", err)
	}
	if !hooks.Any(hooks.EventPreTool) {
		t.Fatal("pre_tool hook not loaded")
	}

	writeProjectHooks(t, wd, postHooksJSON)
	summary, err := reloadProjectSettings(context.Background())
	if err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if hooks.Any(hooks.EventPreTool) {
		t.Error("old pre_tool hook still active after reload")
	}
	if !hooks.Any(hooks.EventPostTool) {
		t.Error("new post_tool hook not active after reload")
	}
	if !strings.Contains(summary, "hooks: 1 loaded") {
		t.Errorf("summary = %q, want hooks line", summary)
	}
}

func TestReloadProjectSettingsKeepsHooksOnBrokenFile(t *testing.T) {
	wd := setupReloadProject(t)
	writeProjectHooks(t, wd, postHooksJSON)
	if _, err := reloadProjectSettings(context.Background()); err != nil {
		t.Fatalf("first reload: %v", err)
	}

	writeProjectHooks(t, wd, `{"hooks": [`)
	summary, err := reloadProjectSettings(context.Background())
	if err == nil {
		t.Fatal("broken hooks.json: want an error")
	}
	if !hooks.Any(hooks.EventPostTool) {
		t.Error("previous hooks dropped by a broken file")
	}
	if !strings.Contains(summary, "hooks: kept previous settings") {
		t.Errorf("summary = %q, want the kept-previous hooks line", summary)
	}
}

func TestReloadProjectSettingsNotSetUp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectSetup.mu.Lock()
	projectSetup.set = false
	projectSetup.mu.Unlock()
	if _, err := reloadProjectSettings(context.Background()); err == nil {
		t.Fatal("want an error when the project is not set up")
	}
}

// TestHandleReloadCommandPrintsSummary checks the /reload output. It reuses
// fakeSlashDisplay from tui_reading_state_test.go.
func TestHandleReloadCommandPrintsSummary(t *testing.T) {
	setupReloadProject(t)
	disp := &fakeSlashDisplay{}
	handleReloadCommand(disp, context.Background())
	if !strings.Contains(disp.lastToolBlock, "hooks: 0 loaded") {
		t.Errorf("ToolBlock = %q, want the hooks summary", disp.lastToolBlock)
	}
	if disp.lastError != nil {
		t.Errorf("unexpected error: %v", disp.lastError)
	}
}

// shortWatchIntervals makes the watcher poll and settle fast for a test.
func shortWatchIntervals(t *testing.T) {
	t.Helper()
	prevPoll, prevDebounce := watchPollInterval, watchDebounce
	watchPollInterval = 2 * time.Millisecond
	watchDebounce = 5 * time.Millisecond
	t.Cleanup(func() {
		watchPollInterval, watchDebounce = prevPoll, prevDebounce
	})
}

func TestWatchSettingsReloadsOnceAfterQuickChanges(t *testing.T) {
	shortWatchIntervals(t)

	var mu sync.Mutex
	state := "v0"
	// baseline is closed when the watcher has read its first fingerprint.
	baseline := make(chan struct{})
	var once sync.Once
	fingerprint := func() string {
		mu.Lock()
		defer mu.Unlock()
		once.Do(func() { close(baseline) })
		return state
	}
	setState := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		state = s
	}

	reloads := make(chan struct{}, 10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchSettings(ctx, fingerprint, func() { reloads <- struct{}{} })
	}()

	select {
	case <-baseline:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not start")
	}
	setState("v1")
	setState("v2")
	setState("v3")

	select {
	case <-reloads:
	case <-time.After(5 * time.Second):
		t.Fatal("no reload after the change")
	}
	select {
	case <-reloads:
		t.Fatal("reload ran twice for quick changes")
	case <-time.After(20 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop on context end")
	}
}

func TestSettingsFingerprintSeesFileChange(t *testing.T) {
	wd := setupReloadProject(t)
	before := settingsFingerprint(wd, true)
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".tyci"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".tyci", "hooks.json"), []byte(preHooksJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := settingsFingerprint(wd, true); after == before {
		t.Fatal("fingerprint did not change after hooks.json was written")
	}
}

func TestSettingsFingerprintSeesMCPConfig(t *testing.T) {
	wd := setupReloadProject(t)
	home := os.Getenv("HOME")
	writeMCP := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	before := settingsFingerprint(wd, true)
	writeMCP(filepath.Join(home, ".tyci", "mcp.json"))
	afterGlobal := settingsFingerprint(wd, true)
	if afterGlobal == before {
		t.Fatal("fingerprint did not change after the global mcp.json was written")
	}

	writeMCP(gitinfo.ProjectPath(wd, "mcp.json"))
	if afterProject := settingsFingerprint(wd, true); afterProject == afterGlobal {
		t.Fatal("fingerprint did not change after the project mcp.json was written")
	}
}

func TestSettingsFingerprintIgnoresUntrustedProjectMCP(t *testing.T) {
	wd := setupReloadProject(t)
	before := settingsFingerprint(wd, false)
	path := gitinfo.ProjectPath(wd, "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := settingsFingerprint(wd, false); after != before {
		t.Fatal("fingerprint changed for the project mcp.json of an untrusted project")
	}
}
