package flow

import (
	"context"
	"testing"

	"github.com/crazy-goat/tyci-agent/tools"
)

const flowMadeSource = `return {
  schema = {name = "flow_made", description = "test", parameters = {}},
  run = function(ctx, args) return {success = true, content = "ok"} end
}`

// A run that ends removes the Lua tools made in its run folder. A paused run keeps them.
func TestEndRunToolsRemovesToolsOfEndedRun(t *testing.T) {
	defer tools.SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	ctx := tools.WithRunDir(context.Background(), dir)
	if res := tools.RunTool(ctx, "tool_create", map[string]any{"name": "flow_made", "source": flowMadeSource}); !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}

	endRunTools("paused", dir)
	if res := tools.RunTool(context.Background(), "flow_made", nil); !res.Success {
		t.Fatal("a paused run lost its tool")
	}

	endRunTools("done", dir)
	if res := tools.RunTool(context.Background(), "flow_made", nil); res.Success {
		t.Fatal("the tool of the ended run is still registered")
	}
}
