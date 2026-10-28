package main

import (
	"context"
	"testing"

	"github.com/crazy-goat/tyci-agent/tools"
)

// The subagent runner is the real gate of a child's tool calls. These tests
// check that tool_create reaches the subagent gate, as main.go wires it.
func TestSubagentToolRunner_ToolCreateRefusedWithoutList(t *testing.T) {
	defer tools.SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	defer tools.RemoveRunTools(dir)

	r := &subagentToolRunner{}
	ctx := tools.WithRunDir(tools.WithDepth(context.Background(), 1), dir)
	_, _ = r.Run(ctx, "tool_create", map[string]any{"name": "main_made", "source": mainCreateSource})
	if res := tools.RunTool(context.Background(), "main_made", nil); res.Success {
		t.Fatal("an unrestricted subagent created a tool")
	}
}

func TestSubagentToolRunner_ToolCreateAllowedWhenListed(t *testing.T) {
	defer tools.SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	defer tools.RemoveRunTools(dir)

	r := &subagentToolRunner{allowed: []string{"tool_create"}}
	ctx := tools.WithRunDir(tools.WithDepth(context.Background(), 1), dir)
	if _, err := r.Run(ctx, "tool_create", map[string]any{"name": "main_made", "source": mainCreateSource}); err != nil {
		t.Fatalf("a listed subagent was refused: %v", err)
	}
	if res := tools.RunTool(context.Background(), "main_made", nil); !res.Success {
		t.Fatalf("the listed subagent's tool is not registered: %s", res.Error)
	}
}

// mainCreateSource is a valid Lua tool script for the tests above.
const mainCreateSource = `return {
  schema = {name = "main_made", description = "test", parameters = {}},
  run = function(ctx, args) return {success = true, content = "ok"} end
}`
