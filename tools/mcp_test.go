package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

// slowCloseClient is a fake mcp.Client whose Close() blocks for a fixed
// delay, so tests can tell a serial Close() (N * delay) apart from a
// concurrent one (~delay regardless of N).
type slowCloseClient struct {
	name  string
	delay time.Duration
}

func (c *slowCloseClient) Initialize(ctx context.Context) error { return nil }
func (c *slowCloseClient) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	return nil, nil
}
func (c *slowCloseClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*mcp.CallToolResult, error) {
	return nil, nil
}
func (c *slowCloseClient) Close() error {
	time.Sleep(c.delay)
	return nil
}
func (c *slowCloseClient) Name() string                                         { return c.name }
func (c *slowCloseClient) SetSamplingHandler(handler mcp.SamplingHandler)       {}
func (c *slowCloseClient) SetElicitationHandler(handler mcp.ElicitationHandler) {}

// TestMCPToolRunnerCloseIsConcurrent covers the reviewer's finding: closing
// three MCP clients that each take a grace period to shut down (e.g. an
// npx-wrapped server that ignores stdin EOF) must not stall the caller for
// the sum of their delays. Close used to iterate r.clients and call
// client.Close() one at a time while holding r.mu, so N slow servers cost
// N * delay and blocked every other MCPToolRunner method meanwhile. It
// must instead close them concurrently, costing roughly one delay no
// matter how many servers there are.
func TestMCPToolRunnerCloseIsConcurrent(t *testing.T) {
	const (
		n     = 4
		delay = 150 * time.Millisecond
	)

	r := NewMCPToolRunner()
	for i := 0; i < n; i++ {
		name := "server"
		r.clients[name+string(rune('0'+i))] = &slowCloseClient{name: name, delay: delay}
	}

	start := time.Now()
	r.Close()
	elapsed := time.Since(start)

	// Serial close of n clients would take roughly n*delay (600ms here);
	// concurrent close should take roughly one delay plus scheduling slop.
	// Use a threshold well below n*delay but comfortably above delay alone.
	threshold := delay * 2
	if elapsed > threshold {
		t.Errorf("MCPToolRunner.Close() took %v for %d clients each with a %v Close(); expected roughly %v (concurrent), not up to %v (serial) -- closes are not running concurrently", elapsed, n, delay, delay, n*delay)
	}
}
