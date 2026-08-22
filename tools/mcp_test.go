package tools

import (
	"context"
	"testing"

	"github.com/decodo/tyci/internal/mcp"
)

// TestMCPToolRunnerRunToolUnknownNameDoesNotPanic covers F8's first
// nil-dereference: RunTool used to look up r.clients[t.server] before
// checking the "ok" returned from r.tools[name], so an unknown tool name
// (t would be a nil *mcpTool, and t.server would deref it) panicked. It was
// safe only because the single production dispatch site guards every call
// with HasTool first. This calls RunTool directly, bypassing that guard, to
// prove RunTool doesn't rely on the caller for safety.
func TestMCPToolRunnerRunToolUnknownNameDoesNotPanic(t *testing.T) {
	r := NewMCPToolRunner()

	res := r.RunTool(context.Background(), "mcp_nosuch_tool", map[string]any{})

	if res.Success {
		t.Fatalf("expected failure for unknown MCP tool, got success: %+v", res)
	}
	if res.Error == "" {
		t.Fatalf("expected an error message for unknown MCP tool, got none: %+v", res)
	}
}

// TestMCPToolsSchemaIsSortedByName covers item 6: MCPToolsSchema used to
// iterate the tools map directly, so the emitted order (and therefore the
// serialized JSON given to the model) varied from call to call. That means
// two spawns of the same subagent could get byte-different tool schemas and
// never share a provider-side prompt-cache prefix. Registering tools in
// reverse-alphabetical order and checking the schema comes back sorted
// catches a regression back to map iteration order (which, for enough
// entries, will not incidentally be sorted).
func TestMCPToolsSchemaIsSortedByName(t *testing.T) {
	r := NewMCPToolRunner()

	names := []string{
		"mcp_srv_zeta",
		"mcp_srv_yankee",
		"mcp_srv_xray",
		"mcp_srv_whiskey",
		"mcp_srv_victor",
		"mcp_srv_uniform",
		"mcp_srv_tango",
		"mcp_srv_sierra",
		"mcp_srv_romeo",
		"mcp_srv_quebec",
		"mcp_srv_papa",
		"mcp_srv_oscar",
	}
	for _, name := range names {
		r.tools[name] = &mcpTool{
			server: "srv",
			tool:   mcp.Tool{Name: name, Description: "d"},
		}
	}

	for attempt := 0; attempt < 5; attempt++ {
		schema := r.MCPToolsSchema()
		if len(schema) != len(names) {
			t.Fatalf("expected %d schema entries, got %d", len(names), len(schema))
		}
		for i := 1; i < len(schema); i++ {
			prevFn := schema[i-1]["function"].(map[string]any)
			curFn := schema[i]["function"].(map[string]any)
			prevName := prevFn["name"].(string)
			curName := curFn["name"].(string)
			if prevName >= curName {
				t.Fatalf("attempt %d: schema not sorted by name: %q came before %q", attempt, prevName, curName)
			}
		}
	}
}
