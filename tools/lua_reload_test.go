package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// luaToolScript returns a minimal Lua tool script named name.
func luaToolScript(name string) string {
	return `return {schema = {name = "` + name + `"}, run = function(ctx, args) return {success = true, content = "ok"} end}`
}

// luaToolRegistered reports whether name is in the tool registry.
func luaToolRegistered(name string) bool {
	toolRegistryMu.RLock()
	defer toolRegistryMu.RUnlock()
	_, ok := toolRegistry[name]
	return ok
}

func TestReloadLuaToolsAddsAndRemovesFileTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restore := SnapshotLuaToolsForTesting()
	t.Cleanup(restore)
	local := t.TempDir()

	if err := os.WriteFile(filepath.Join(local, "reload_a.lua"), []byte(luaToolScript("reload-a")), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := ReloadLuaTools(local)
	if err != nil || n != 1 {
		t.Fatalf("first reload: n=%d err=%v, want 1 and nil", n, err)
	}
	if !luaToolRegistered("reload-a") {
		t.Fatal("reload-a not registered")
	}

	if err := os.Remove(filepath.Join(local, "reload_a.lua")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "reload_b.lua"), []byte(luaToolScript("reload-b")), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err = ReloadLuaTools(local)
	if err != nil || n != 1 {
		t.Fatalf("second reload: n=%d err=%v, want 1 and nil", n, err)
	}
	if luaToolRegistered("reload-a") {
		t.Error("removed tool reload-a still registered")
	}
	if !luaToolRegistered("reload-b") {
		t.Error("added tool reload-b not registered")
	}
}

func TestReloadLuaToolsKeepsToolsOnBrokenScript(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restore := SnapshotLuaToolsForTesting()
	t.Cleanup(restore)
	local := t.TempDir()

	if err := os.WriteFile(filepath.Join(local, "keep.lua"), []byte(luaToolScript("reload-keep")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReloadLuaTools(local); err != nil {
		t.Fatalf("first reload: %v", err)
	}

	if err := os.WriteFile(filepath.Join(local, "broken.lua"), []byte("return {"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReloadLuaTools(local); err == nil {
		t.Fatal("broken script: want an error")
	}
	if !luaToolRegistered("reload-keep") {
		t.Error("previous tool dropped by a broken script")
	}
}

func TestReloadLuaToolsKeepsOnlyBrokenFileTool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restore := SnapshotLuaToolsForTesting()
	t.Cleanup(restore)
	local := t.TempDir()

	for _, name := range []string{"reload-a", "reload-b"} {
		file := filepath.Join(local, name+".lua")
		if err := os.WriteFile(file, []byte(luaToolScript(name)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReloadLuaTools(local); err != nil {
		t.Fatalf("first reload: %v", err)
	}

	// reload-a breaks, reload-c is added. Only reload-a keeps its old tool.
	if err := os.WriteFile(filepath.Join(local, "reload-a.lua"), []byte("return {"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "reload-c.lua"), []byte(luaToolScript("reload-c")), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := ReloadLuaTools(local)
	if err == nil {
		t.Fatal("broken script: want an error")
	}
	if !strings.Contains(err.Error(), "reload-a.lua") {
		t.Errorf("error %q does not name the broken file", err)
	}
	if n != 3 {
		t.Errorf("n = %d, want 3 (kept reload-a, reload-b, new reload-c)", n)
	}
	for _, name := range []string{"reload-a", "reload-b", "reload-c"} {
		if !luaToolRegistered(name) {
			t.Errorf("%s not registered after a reload with one broken file", name)
		}
	}
}
