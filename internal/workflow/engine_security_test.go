package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEngine_LuaStdlibIsRestricted is the regression test for the security
// fix: before this, Engine.Run built its Lua state with a bare
// lua.NewState(), the full gopher-lua standard library open, so a
// project-local .tyci/agents/*.lua script could reach the filesystem and a
// shell directly (io.open, os.execute) — bypassing every tool-layer
// protection (pre_tool/post_tool hooks, the write-freshness guard, tool
// gating) the same way an unrestricted "lua" tool script would have before
// tools/lua_eval.go's restrictLuaStdlib existed. NewEngine now applies the
// exported tools.RestrictLuaStdlib to its Lua state.
func TestEngine_LuaStdlibIsRestricted(t *testing.T) {
	dir := t.TempDir()
	canaryPath := filepath.Join(dir, "canary.txt")

	cases := []struct {
		name   string
		script string
	}{
		{
			name:   "io.open is unavailable",
			script: `return tostring(io)`,
		},
		{
			name: "io.open cannot write a file",
			script: `
				local f = io.open("` + strings.ReplaceAll(canaryPath, `\`, `\\`) + `", "w")
				if f then
					f:write("should not get here")
					f:close()
				end
				return "ran"
			`,
		},
		{
			name:   "os.execute is unavailable",
			script: `return tostring(os.execute)`,
		},
		{
			name:   "require is unavailable",
			script: `return tostring(require)`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := NewEngine(context.Background(), "prompt")
			defer engine.L.Close()
			engine.registerTyciAPI()

			if err := engine.L.DoString(tc.script); err != nil {
				// A script that raises trying to index/call a nil global
				// (io, os.execute, require) also proves the restriction —
				// either outcome (nil value returned, or an error raised)
				// demonstrates the stdlib entry is gone.
				return
			}
			ret := engine.L.Get(-1)
			engine.L.Pop(1)
			if s := ret.String(); s != "nil" {
				t.Errorf("script %q returned %q, want it to be unable to use the restricted global (io/os.execute/require)", tc.script, s)
			}
		})
	}

	if _, err := os.Stat(canaryPath); !os.IsNotExist(err) {
		t.Fatalf("canary file %s exists — io.open was reachable from an engine-run script", canaryPath)
	}
}

// TestEngine_LuaVMHonorsContextCancellation is the regression test for
// wiring L.SetContext(ctx) into the engine's Lua state (unlike before, when
// nothing let a runaway `while true do end` in a workflow script be
// cancelled). gopher-lua checks the context between VM instructions, so a
// cancelled context aborts a tight loop instead of hanging the process.
func TestEngine_LuaVMHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	engine := NewEngine(ctx, "prompt")
	defer engine.L.Close()

	done := make(chan error, 1)
	go func() {
		done <- engine.L.DoString(`while true do end`)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the cancelled context to abort the infinite loop with an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("infinite loop was not aborted by context cancellation within 5s — L.SetContext is not wired")
	}
}
