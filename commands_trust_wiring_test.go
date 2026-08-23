package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/decodo/tyci/internal/hooks"
	"github.com/decodo/tyci/internal/trust"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/tools"
)

// This file covers item 23's trust gate as wired into initCommon
// (commands.go): a project without a recorded trust decision must run with
// its project-local .tyci/hooks.json and .tyci/tools/*.lua ignored, while
// ~/.tyci content (global hooks, global Lua tools) keeps loading exactly as
// before. A pre-recorded "trusted" decision must load both.
//
// The two tests below share one process (as every test in this package
// does) and therefore one Lua tool registry: TestInitCommon_UntrustedProject
// runs first (Go preserves source order within a file) and never registers
// "local-trust-wiring-tool" at all, and TestInitCommon_TrustedProject is the
// only test that ever does, so neither leaks state the other depends on.

const trustWiringGlobalLuaScript = `return {
  schema = { name = "global-trust-wiring-tool", description = "d", parameters = {} },
  run = function(ctx, args) return {success = true, content = "global"} end
}`

const trustWiringLocalLuaScript = `return {
  schema = { name = "local-trust-wiring-tool", description = "d", parameters = {} },
  run = function(ctx, args) return {success = true, content = "local"} end
}`

// writeTrustWiringHome sets HOME to a fresh temp dir with a global hooks.json
// (blocks "write") and a global Lua tool, mirroring content that must always
// load regardless of any project's trust decision.
func writeTrustWiringHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	tyci := filepath.Join(home, ".tyci")
	if err := os.MkdirAll(filepath.Join(tyci, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tyci, "providers.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}
	globalHooks := `{"hooks":[{"event":"pre_tool","name":"global-hook","tools":["write"],"command":"exit 1"}]}`
	if err := os.WriteFile(filepath.Join(tyci, "hooks.json"), []byte(globalHooks), 0o600); err != nil {
		t.Fatalf("write global hooks.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tyci, "tools", "global.lua"), []byte(trustWiringGlobalLuaScript), 0o644); err != nil {
		t.Fatalf("write global lua tool: %v", err)
	}
}

// writeTrustWiringProject creates a fresh project directory with a
// project-local .tyci/hooks.json (blocks "read") and a project-local Lua
// tool, and chdir's the test into it (restored via t.Cleanup).
func writeTrustWiringProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tyci := filepath.Join(dir, ".tyci")
	if err := os.MkdirAll(filepath.Join(tyci, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	localHooks := `{"hooks":[{"event":"pre_tool","name":"local-hook","tools":["read"],"command":"exit 1"}]}`
	if err := os.WriteFile(filepath.Join(tyci, "hooks.json"), []byte(localHooks), 0o600); err != nil {
		t.Fatalf("write local hooks.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tyci, "tools", "local.lua"), []byte(trustWiringLocalLuaScript), 0o644); err != nil {
		t.Fatalf("write local lua tool: %v", err)
	}

	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return dir
}

func schemaNames(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var schema []map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return schema
}

func TestInitCommon_UntrustedProject_SkipsLocalHooksAndLua_KeepsGlobal(t *testing.T) {
	writeTrustWiringHome(t)
	writeTrustWiringProject(t)
	// Production loads ~/.tyci/tools exactly once, unconditionally, from the
	// tools package's own init() at process startup — long before HOME could
	// be pointed at this test's fixture. Calling the same unconditional
	// loader here exercises it against the fixture directly; initCommon
	// itself never calls it again.
	tools.LoadAndRegisterLuaTools()
	defer hooks.SetForTesting(nil)()

	prov := &fakeProvider{name: "trust-wiring-prov-1", configured: true, models: []string{"m1"}}
	providers.Register(prov)

	cmd := newInitCommonTestCmd()
	if err := cmd.Flags().Set("model", "trust-wiring-prov-1/m1"); err != nil {
		t.Fatalf("set model flag: %v", err)
	}
	if err := cmd.Flags().Set("no-mcp", "true"); err != nil {
		t.Fatalf("set no-mcp flag: %v", err)
	}

	// interactive=false: exercises the non-interactive "never seen before ->
	// defaults to untrusted, never blocks" path.
	_, _, cfg, _, _, _, _, dl, shutdown, err := initCommon(cmd, false, false)
	if err != nil {
		t.Fatalf("initCommon: %v", err)
	}
	defer shutdown()
	if dl != nil {
		defer dl.Close()
	}

	if blocked, _ := hooks.RunPre(context.Background(), "write", map[string]any{"path": "x"}); !blocked {
		t.Fatal("expected the global hook (blocks write) to still be active for an untrusted project")
	}
	if blocked, _ := hooks.RunPre(context.Background(), "read", map[string]any{"path": "x"}); blocked {
		t.Fatal("expected the project-local hook (blocks read) to be skipped for an untrusted project")
	}

	schema := schemaNames(t, cfg.Schema)
	if !schemaHasFunctionNamed(schema, "global-trust-wiring-tool") {
		t.Fatal("expected the global Lua tool to be registered regardless of trust")
	}
	if schemaHasFunctionNamed(schema, "local-trust-wiring-tool") {
		t.Fatal("expected the project-local Lua tool to be skipped for an untrusted project")
	}
}

func TestInitCommon_TrustedProject_LoadsLocalHooksAndLuaToo(t *testing.T) {
	writeTrustWiringHome(t)
	writeTrustWiringProject(t)
	tools.LoadAndRegisterLuaTools() // see the sibling test's comment on this call
	defer hooks.SetForTesting(nil)()

	// Resolved via os.Getwd() (post-chdir), not the t.TempDir() string
	// directly: on macOS the temp dir is reached through a symlink
	// (/tmp -> /private/tmp), and initCommon itself computes its project key
	// from os.Getwd() inside the process, so the test must key its
	// SetTrusted call the exact same way to land on the same trust.json
	// entry initCommon will look up.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root, err := session.ProjectKey(wd)
	if err != nil {
		t.Fatalf("ProjectKey: %v", err)
	}
	if err := trust.SetTrusted(root, true); err != nil {
		t.Fatalf("SetTrusted: %v", err)
	}

	prov := &fakeProvider{name: "trust-wiring-prov-2", configured: true, models: []string{"m1"}}
	providers.Register(prov)

	cmd := newInitCommonTestCmd()
	if err := cmd.Flags().Set("model", "trust-wiring-prov-2/m1"); err != nil {
		t.Fatalf("set model flag: %v", err)
	}
	if err := cmd.Flags().Set("no-mcp", "true"); err != nil {
		t.Fatalf("set no-mcp flag: %v", err)
	}

	_, _, cfg, _, _, _, _, dl, shutdown, err := initCommon(cmd, false, false)
	if err != nil {
		t.Fatalf("initCommon: %v", err)
	}
	defer shutdown()
	if dl != nil {
		defer dl.Close()
	}

	if blocked, _ := hooks.RunPre(context.Background(), "write", map[string]any{"path": "x"}); !blocked {
		t.Fatal("expected the global hook to still be active for a trusted project")
	}
	if blocked, _ := hooks.RunPre(context.Background(), "read", map[string]any{"path": "x"}); !blocked {
		t.Fatal("expected the project-local hook to now be active for a trusted project")
	}

	schema := schemaNames(t, cfg.Schema)
	if !schemaHasFunctionNamed(schema, "local-trust-wiring-tool") {
		t.Fatal("expected the project-local Lua tool to be registered for a trusted project")
	}
}
