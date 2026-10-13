package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
