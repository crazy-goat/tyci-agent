package workflow

import (
	"testing"

	lua "github.com/yuin/gopher-lua"

	"github.com/decodo/tyci/internal/agentdefs"
)

// TestLuaAgents_ReturnsConfiguredAgents verifies that tyci.agents() in a Lua
// workflow script returns the names of agent definitions visible from the
// current working directory.
func TestLuaAgents_ReturnsConfiguredAgents(t *testing.T) {
	engine := NewEngine(t.Context(), "test prompt")

	// Invoke the tyci.agents() entry point directly and inspect the pushed
	// return value on the Lua stack.
	engine.luaAgents(engine.L)

	arr, ok := engine.L.Get(-1).(*lua.LTable)
	engine.L.Pop(1)
	if !ok {
		t.Fatal("tyci.agents must push a Lua table")
	}

	// The result should mirror List("").
	want := agentdefs.List("")
	if arr.Len() != len(want) {
		t.Fatalf("agent count = %d, want %d", arr.Len(), len(want))
	}

	got := map[string]bool{}
	arr.ForEach(func(k, v lua.LValue) {
		if s, ok := v.(lua.LString); ok {
			got[string(s)] = true
		}
	})
	if len(got) == 0 {
		t.Fatal("tyci.agents should list at least the built-in agents")
	}

	for _, def := range want {
		if !got[def.Name] {
			t.Errorf("tyci.agents is missing agent %q", def.Name)
		}
	}
}
