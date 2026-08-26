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

// fakeMCPClient is a minimal mcp.Client double: no network, no process,
// just enough behavior to install directly into an MCPToolRunner's private
// fields (legal from within package tools) for schema/gate tests below.
type fakeMCPClient struct {
	name   string
	closed bool
}

func (f *fakeMCPClient) Initialize(ctx context.Context) error              { return nil }
func (f *fakeMCPClient) ListTools(ctx context.Context) ([]mcp.Tool, error) { return nil, nil }
func (f *fakeMCPClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*mcp.CallToolResult, error) {
	return nil, nil
}
func (f *fakeMCPClient) Close() error                                   { f.closed = true; return nil }
func (f *fakeMCPClient) Name() string                                   { return f.name }
func (f *fakeMCPClient) SetSamplingHandler(h mcp.SamplingHandler)       {}
func (f *fakeMCPClient) SetElicitationHandler(h mcp.ElicitationHandler) {}

// withRunner installs runner as the global MCP runner for the duration of
// the test and restores whatever was there before on cleanup, since
// GetMCPToolRunner/RunTool/GetSubagentToolsSchemaJSONFor all read the
// package-level global rather than taking a runner as a parameter.
func withRunner(t *testing.T, runner *MCPToolRunner) {
	t.Helper()
	restore := SetMCPToolRunnerForTests(runner)
	t.Cleanup(restore)
}

// newTestRunnerWithTool builds a runner with one connected fake server and
// one registered tool "mcp_<server>_<toolName>".
func newTestRunnerWithTool(server, toolName string) *MCPToolRunner {
	r := NewMCPToolRunner()
	r.clients[server] = &fakeMCPClient{name: server}
	prefixed := "mcp_" + server + "_" + toolName
	r.tools[prefixed] = &mcpTool{
		server: server,
		tool:   mcp.Tool{Name: toolName, Description: "does " + toolName, InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	return r
}

// schemaHasTool reports whether schema (as produced by GetToolsSchema-style
// functions) contains a function tool named name.
func schemaHasTool(t *testing.T, schema []map[string]any, name string) bool {
	t.Helper()
	for _, s := range schema {
		fn, ok := s["function"].(map[string]any)
		if !ok {
			continue
		}
		if n, _ := fn["name"].(string); n == name {
			return true
		}
	}
	return false
}

// --- decision (a): an MCP tool is not auto-granted to a whitelisted subagent ---

// TestGetSubagentToolsSchemaJSONFor_Unrestricted_IncludesMCPTools covers the
// "no whitelist keeps everything, as today" half of decision (a): with no
// tools: list at all, a connected server's tool must still be offered.
func TestGetSubagentToolsSchemaJSONFor_Unrestricted_IncludesMCPTools(t *testing.T) {
	withRunner(t, newTestRunnerWithTool("weather", "forecast"))

	var schema []map[string]any
	if err := json.Unmarshal(GetSubagentToolsSchemaJSONFor(nil), &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !schemaHasTool(t, schema, "mcp_weather_forecast") {
		t.Fatalf("expected an unrestricted subagent to be offered mcp_weather_forecast")
	}
}

// TestGetSubagentToolsSchemaJSONFor_Restricted_OmitsUnlistedMCPTool is the
// test that fails if the wrong-as-a-default blanket MCP exemption (every
// agent gets every tool on every server, reviewed away in batch 2) is
// reintroduced: a subagent whose tools: list names neither the tool nor a
// wildcard for its server must NOT be offered a connected server's tool,
// write-capable or not.
func TestGetSubagentToolsSchemaJSONFor_Restricted_OmitsUnlistedMCPTool(t *testing.T) {
	withRunner(t, newTestRunnerWithTool("weather", "forecast"))

	var schema []map[string]any
	if err := json.Unmarshal(GetSubagentToolsSchemaJSONFor([]string{"read"}), &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if schemaHasTool(t, schema, "mcp_weather_forecast") {
		t.Fatalf("a plain tools: whitelist that never named mcp_weather_forecast (or a wildcard for it) must not be offered it")
	}
}

// TestGetSubagentToolsSchemaJSONFor_Restricted_ExplicitEntryIncluded covers
// the "honour explicit entries" half of decision (a): an agent definition
// authored after a server is known to exist can name one of its tools
// literally.
func TestGetSubagentToolsSchemaJSONFor_Restricted_ExplicitEntryIncluded(t *testing.T) {
	withRunner(t, newTestRunnerWithTool("weather", "forecast"))

	var schema []map[string]any
	if err := json.Unmarshal(GetSubagentToolsSchemaJSONFor([]string{"read", "mcp_weather_forecast"}), &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !schemaHasTool(t, schema, "mcp_weather_forecast") {
		t.Fatalf("an explicitly whitelisted mcp_weather_forecast must be offered")
	}
}

// TestGetSubagentToolsSchemaJSONFor_Restricted_WildcardIncludesOnlyItsServer
// covers the "per-server wildcard the definition opts into" half of
// decision (a), and that it is scoped to the named server only — a
// wildcard for "weather" must not leak tools from an unrelated "other"
// server that happens to also be connected.
func TestGetSubagentToolsSchemaJSONFor_Restricted_WildcardIncludesOnlyItsServer(t *testing.T) {
	r := newTestRunnerWithTool("weather", "forecast")
	r.clients["other"] = &fakeMCPClient{name: "other"}
	r.tools["mcp_other_secret"] = &mcpTool{server: "other", tool: mcp.Tool{Name: "secret"}}
	withRunner(t, r)

	var schema []map[string]any
	if err := json.Unmarshal(GetSubagentToolsSchemaJSONFor([]string{"read", "mcp_weather_*"}), &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !schemaHasTool(t, schema, "mcp_weather_forecast") {
		t.Fatalf("mcp_weather_* wildcard should include mcp_weather_forecast")
	}
	if schemaHasTool(t, schema, "mcp_other_secret") {
		t.Fatalf("mcp_weather_* wildcard must not include a different server's tool")
	}
}

// TestAllowOnlySubagent_MatchesSchema_ExplicitAndWildcard is the runtime-
// gate mirror of the schema tests above: whatever GetSubagentToolsSchemaJSONFor
// offers, AllowOnlySubagent must actually let through, and whatever it
// omits, the gate must actually refuse -- an "advertised but refused" (or
// worse, "not advertised but callable") tool is exactly the class of bug
// item 8's schema/gate split is meant to prevent.
func TestAllowOnlySubagent_MatchesSchema_ExplicitAndWildcard(t *testing.T) {
	withRunner(t, newTestRunnerWithTool("weather", "forecast"))

	// No MCP entry at all: the tool must be refused.
	plain := AllowOnlySubagent([]string{"read"})
	if err := plain("mcp_weather_forecast"); err == nil {
		t.Fatalf("expected a plain whitelist to refuse an unlisted MCP tool")
	}

	// Explicit entry: permitted.
	explicit := AllowOnlySubagent([]string{"read", "mcp_weather_forecast"})
	if err := explicit("mcp_weather_forecast"); err != nil {
		t.Fatalf("expected an explicitly whitelisted MCP tool to be permitted, got: %v", err)
	}

	// Wildcard: permitted for the named server, refused for another.
	wildcard := AllowOnlySubagent([]string{"read", "mcp_weather_*"})
	if err := wildcard("mcp_weather_forecast"); err != nil {
		t.Fatalf("expected mcp_weather_* to permit mcp_weather_forecast, got: %v", err)
	}
	if err := wildcard("mcp_other_secret"); err == nil {
		t.Fatalf("expected mcp_weather_* to still refuse a different server's tool")
	}
}

// TestMCPWildcard_DoesNotMatchServerWithSharedPrefix is the regression test
// for the reviewer's finding: mcp_weather_* used to grant any tool whose
// FLATTENED name started with "mcp_weather_", which also matches an
// unrelated server literally named "weather_api" ("mcp_weather_api_..."
// starts with "mcp_weather_" too). The fix resolves a tool's OWNING server
// via the runner (serverFor) and requires an exact match, so "weather" ==
// "weather_api" must be false on both the schema and the gate side.
func TestMCPWildcard_DoesNotMatchServerWithSharedPrefix(t *testing.T) {
	r := newTestRunnerWithTool("weather", "forecast")
	r.clients["weather_api"] = &fakeMCPClient{name: "weather_api"}
	r.tools["mcp_weather_api_delete_all"] = &mcpTool{server: "weather_api", tool: mcp.Tool{Name: "delete_all"}}
	withRunner(t, r)

	allowed := []string{"read", "mcp_weather_*"}

	var schema []map[string]any
	if err := json.Unmarshal(GetSubagentToolsSchemaJSONFor(allowed), &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if schemaHasTool(t, schema, "mcp_weather_api_delete_all") {
		t.Fatalf("mcp_weather_* must not match server %q merely because its flattened tool names start with the same characters as %q", "weather_api", "weather")
	}
	if !schemaHasTool(t, schema, "mcp_weather_forecast") {
		t.Fatalf("mcp_weather_* should still include the actual weather server's own tool")
	}

	gate := AllowOnlySubagent(allowed)
	if err := gate("mcp_weather_api_delete_all"); err == nil {
		t.Fatalf("gate must refuse a different server's tool even when its name shares the wildcard's prefix")
	}
	if err := gate("mcp_weather_forecast"); err != nil {
		t.Fatalf("gate should still permit the actual wildcarded server's tool, got: %v", err)
	}
}

// TestMCPWildcard_BareMCPStarIsNotAGlobalGrant covers the other half of the
// reviewer's finding: "mcp_*" with no server name in it must not act as
// "every tool on every server" -- that is exactly the blanket exemption
// decision (a) removed. A bare mcp_* is rejected (ignored), not treated as
// a wildcard over everything.
func TestMCPWildcard_BareMCPStarIsNotAGlobalGrant(t *testing.T) {
	r := newTestRunnerWithTool("weather", "forecast")
	r.clients["billing"] = &fakeMCPClient{name: "billing"}
	r.tools["mcp_billing_charge"] = &mcpTool{server: "billing", tool: mcp.Tool{Name: "charge"}}
	withRunner(t, r)

	allowed := []string{"read", "mcp_*"}

	var schema []map[string]any
	if err := json.Unmarshal(GetSubagentToolsSchemaJSONFor(allowed), &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if schemaHasTool(t, schema, "mcp_billing_charge") {
		t.Fatalf("bare mcp_* must not grant a tool on an unnamed server")
	}
	if schemaHasTool(t, schema, "mcp_weather_forecast") {
		t.Fatalf("bare mcp_* must not grant a tool on any server -- it names no server at all")
	}

	gate := AllowOnlySubagent(allowed)
	if err := gate("mcp_billing_charge"); err == nil {
		t.Fatalf("bare mcp_* must not make the gate permit an unnamed server's tool")
	}
	if err := gate("mcp_weather_forecast"); err == nil {
		t.Fatalf("bare mcp_* must not make the gate permit any MCP tool")
	}
}

// --- clean shutdown ---

// TestShutdownMCP_ClosesEveryConnectedClient is the test that fails if the
// InitMCP/ShutdownMCP wiring is reverted: ShutdownMCP must exist and must
// close every client the runner knows about, not just be a doc comment.
func TestShutdownMCP_ClosesEveryConnectedClient(t *testing.T) {
	a := &fakeMCPClient{name: "a"}
	b := &fakeMCPClient{name: "b"}
	r := NewMCPToolRunner()
	r.clients["a"] = a
	r.clients["b"] = b
	withRunner(t, r)

	ShutdownMCP()

	if !a.closed || !b.closed {
		t.Fatalf("expected both clients closed, got a.closed=%v b.closed=%v", a.closed, b.closed)
	}
}

func TestShutdownMCP_NilRunner_NoPanic(t *testing.T) {
	withRunner(t, nil)
	ShutdownMCP() // must not panic
}

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
