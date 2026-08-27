package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	lua "github.com/yuin/gopher-lua"

	"github.com/decodo/tyci/internal/agentdefs"
)

// newSessionTable is a small test helper: calls tyci.new_session(model,
// opts) through the engine, returning both the pushed Lua table and the
// underlying *luaSession the engine registered for it.
func newSessionTable(t *testing.T, e *Engine, model string, opts *lua.LTable) (*lua.LTable, *luaSession) {
	t.Helper()
	e.L.Push(e.L.NewFunction(e.luaNewSession))
	e.L.Push(lua.LString(model))
	if opts != nil {
		e.L.Push(opts)
	} else {
		e.L.Push(lua.LNil)
	}
	if err := e.L.PCall(2, 1, nil); err != nil {
		t.Fatalf("tyci.new_session failed: %v", err)
	}
	v := e.L.Get(-1)
	e.L.Pop(1)
	tbl, ok := v.(*lua.LTable)
	if !ok {
		t.Fatalf("tyci.new_session must push a table, got %T", v)
	}
	key := e.L.GetField(tbl, "_session_key").String()
	sess, ok := e.sessions[key]
	if !ok {
		t.Fatalf("session %q not registered in engine.sessions", key)
	}
	return tbl, sess
}

// TestNewSession_DefaultMaxIterations verifies that a session created
// without any options gets the same default MaxIterations engine.go
// hardcoded (10) before this became configurable, so existing scripts keep
// their current behavior unless they opt into something else.
func TestNewSession_DefaultMaxIterations(t *testing.T) {
	engine := NewEngine(t.Context(), "prompt")
	_, sess := newSessionTable(t, engine, "some/model", nil)

	if sess.maxIterations != defaultSessionMaxIterations {
		t.Errorf("maxIterations = %d, want default %d", sess.maxIterations, defaultSessionMaxIterations)
	}
}

// TestNewSession_ExplicitMaxIterations verifies that
// tyci.new_session(model, {max_iterations = N}) overrides the default —
// the "script-settable" half of making MaxIterations configurable.
func TestNewSession_ExplicitMaxIterations(t *testing.T) {
	engine := NewEngine(t.Context(), "prompt")
	opts := engine.L.NewTable()
	engine.L.SetField(opts, "max_iterations", lua.LNumber(3))

	_, sess := newSessionTable(t, engine, "some/model", opts)

	if sess.maxIterations != 3 {
		t.Errorf("maxIterations = %d, want 3", sess.maxIterations)
	}
}

// TestNewSession_MaxIterationsFromNamedAgent verifies the other half: an
// agent name passed as opts.agent sources MaxIterations from that agent
// definition's frontmatter — the same place per-agent config values come
// from everywhere else in the app (internal/agentdefs.Def.MaxIterations) —
// instead of a value hardcoded in the engine.
func TestNewSession_MaxIterationsFromNamedAgent(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, ".tyci", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("mkdir agents dir: %v", err)
	}
	def := "---\nmodel: some/model\nmax_iterations: 7\n---\nA test agent.\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "wf-test-agent.md"), []byte(def), 0644); err != nil {
		t.Fatalf("write agent def: %v", err)
	}

	// Sanity check the fixture parses the way the test expects before
	// relying on it through sessionOptions/agentdefs.Get("", ...) below,
	// which resolves relative to the process's actual working directory.
	if got, err := agentdefs.Parse("wf-test-agent.md", []byte(def)); err != nil || got.MaxIterations != 7 {
		t.Fatalf("fixture agent def did not parse as expected: %+v, err=%v", got, err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	engine := NewEngine(context.Background(), "prompt")
	opts := engine.L.NewTable()
	engine.L.SetField(opts, "agent", lua.LString("wf-test-agent"))

	_, sess := newSessionTable(t, engine, "", opts)

	if sess.maxIterations != 7 {
		t.Errorf("maxIterations = %d, want 7 (from agent frontmatter)", sess.maxIterations)
	}
	if sess.model != "some/model" {
		t.Errorf("model = %q, want %q (from agent frontmatter, since none was passed positionally)", sess.model, "some/model")
	}
}
