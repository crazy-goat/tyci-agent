package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/tools"
	"github.com/spf13/cobra"
)

// This file is item 8 batch 2's finding (d): initCommon had no test
// coverage at all, so nothing failed when the wiring itself (the
// tools.InitMCP call, or the GetToolsSchemaJSON -> GetAllToolsSchemaJSON
// switch) was missing or reverted. Both are exercised here through
// initCommon's real production code path, not a reimplementation of it.

// requireShForWiring skips the test if there's no "sh" on this platform to
// drive the fake MCP server script below (mirrors internal/mcp's
// requireSh, which is unexported and package-private there).
func requireShForWiring(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available on this platform")
	}
}

// newInitCommonTestCmd builds a *cobra.Command carrying exactly the flags
// initCommon reads, with the same defaults rootCmd registers in its
// init(). A fresh command (rather than reusing the real runCmd/rootCmd) so
// this test can't leak flag state into any other test that shares the
// same global command objects.
func newInitCommonTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("model", "", "")
	cmd.Flags().String("agent", "", "")
	cmd.Flags().Int("max-retries", 5, "")
	cmd.Flags().Int("max-iterations", -1, "")
	cmd.Flags().Int("max-tokens", 0, "")
	cmd.Flags().String("history-file", "", "")
	cmd.Flags().String("session", "", "")
	cmd.Flags().Bool("no-session", true, "")
	cmd.Flags().Bool("debug", false, "")
	cmd.Flags().Bool("no-debug", true, "")
	cmd.Flags().Bool("no-mcp", false, "")
	return cmd
}

// mcpFakeServerScript is a minimal fake stdio MCP server: it answers
// initialize with id 1 and tools/list with id 2, replying to each request
// only once it actually sees that request's method on stdin. It must read
// before it answers rather than printing both responses up front — a
// server that echoes eagerly can have readLoop consume both lines before
// ListTools has even registered id 2's pending channel, so its response is
// silently dropped (readLoop only dispatches a response whose id has a
// waiting caller) and the call spuriously times out. That failure mode has
// nothing to do with the code under test, so this script is deliberately
// request-driven instead.
const mcpFakeServerScript = `while IFS= read -r line; do
case "$line" in
	*'"method":"initialize"'*) printf '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"fake","version":"1.0"}}}\n' ;;
	*'"method":"tools/list"'*) printf '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"forecast","description":"d","inputSchema":{"type":"object"}}]}}\n' ;;
esac
done`

// writeWiringTestHome sets HOME to a fresh temp directory, drops a
// providers.json (so registerProviders' models.dev fetch inside
// initCommon short-circuits instead of hitting the network — the test
// resolves its model via a directly-registered fake provider instead) and
// an mcp.json configuring one stdio server running mcpFakeServerScript.
func writeWiringTestHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".tyci"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".tyci", "providers.json"), []byte("{}"), 0600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}
	scriptJSON, _ := json.Marshal(mcpFakeServerScript)
	mcpConfig := `{"mcpServers":{"weather":{"command":"sh","args":["-c",` + string(scriptJSON) + `]}}}`
	if err := os.WriteFile(filepath.Join(dir, ".tyci", "mcp.json"), []byte(mcpConfig), 0600); err != nil {
		t.Fatalf("write mcp.json: %v", err)
	}
}

// TestInitCommon_ConnectMCPTrue_ConnectsAndAdvertisesServerTools is the
// test for finding (d): it fails if initCommon stops calling tools.InitMCP
// (the connected server, and its tool, would never appear) or if commands.go
// reverts to tools.GetToolsSchemaJSON() instead of tools.
// GetTopLevelToolsSchemaJSON() (the schema handed to the model would then
// omit the connected tool even though InitMCP ran). GetTopLevelToolsSchemaJSON,
// not GetAllToolsSchemaJSON, is what the top-level agent.Config actually gets
// now (item 29): it is GetAllToolsSchemaJSON with ask_parent removed, since
// the top-level conversation is not itself a job and ask_parent always fails
// immediately there.
func TestInitCommon_ConnectMCPTrue_ConnectsAndAdvertisesServerTools(t *testing.T) {
	requireShForWiring(t)
	writeWiringTestHome(t)

	prov := &fakeProvider{name: "wiretest-prov", configured: true, models: []string{"m1"}}
	providers.Register(prov)

	restore := tools.SetMCPToolRunnerForTests(nil)
	t.Cleanup(func() {
		tools.ShutdownMCP()
		restore()
	})

	cmd := newInitCommonTestCmd()
	if err := cmd.Flags().Set("model", "wiretest-prov/m1"); err != nil {
		t.Fatalf("set model flag: %v", err)
	}

	_, _, cfg, _, _, _, _, dl, shutdown, err := initCommon(cmd, true, false)
	if err != nil {
		t.Fatalf("initCommon: %v", err)
	}
	t.Cleanup(shutdown)
	if dl != nil {
		t.Cleanup(func() { dl.Close() })
	}

	if tools.GetMCPToolRunner() == nil {
		t.Fatalf("expected initCommon(cmd, true, false) to have called tools.InitMCP and left a runner connected")
	}
	if !tools.GetMCPToolRunner().HasTool("mcp_weather_forecast") {
		t.Fatalf("expected the configured weather server's forecast tool to be connected")
	}

	var schema []map[string]any
	if err := json.Unmarshal(cfg.Schema, &schema); err != nil {
		t.Fatalf("unmarshal cfg.Schema: %v", err)
	}
	if !schemaHasFunctionNamed(schema, "mcp_weather_forecast") {
		t.Fatalf("expected cfg.Schema (from initCommon) to include mcp_weather_forecast; got %d tools", len(schema))
	}

	// cfg.Schema must be exactly what GetTopLevelToolsSchemaJSON reports
	// right now, not the builtin-only GetToolsSchemaJSON — this is the
	// specific switch finding (d) calls out.
	want := tools.GetTopLevelToolsSchemaJSON()
	if !bytes.Equal(cfg.Schema, want) {
		t.Fatalf("cfg.Schema does not match tools.GetTopLevelToolsSchemaJSON(): initCommon is not using it")
	}
	builtinOnly := tools.GetToolsSchemaJSON()
	if bytes.Equal(cfg.Schema, builtinOnly) {
		t.Fatalf("cfg.Schema equals the builtin-only GetToolsSchemaJSON() — MCP tools were not included")
	}
	if schemaHasFunctionNamed(schema, "ask_parent") {
		t.Fatalf("expected the top-level agent's schema to exclude ask_parent — the top-level conversation has no job id, so it always fails immediately")
	}
}

// TestInitCommon_ConnectMCPFalse_DoesNotConnect is the companion negative
// case: connectMCP=false (what --no-mcp forces, whatever the caller's own
// default is) must not touch tools.InitMCP at all, so a configured server
// is never even dialed.
func TestInitCommon_ConnectMCPFalse_DoesNotConnect(t *testing.T) {
	requireShForWiring(t)
	writeWiringTestHome(t)

	prov := &fakeProvider{name: "wiretest-prov-2", configured: true, models: []string{"m1"}}
	providers.Register(prov)

	restore := tools.SetMCPToolRunnerForTests(nil)
	t.Cleanup(func() {
		tools.ShutdownMCP()
		restore()
	})

	cmd := newInitCommonTestCmd()
	if err := cmd.Flags().Set("model", "wiretest-prov-2/m1"); err != nil {
		t.Fatalf("set model flag: %v", err)
	}

	_, _, cfg, _, _, _, _, dl, shutdown, err := initCommon(cmd, false, false)
	if err != nil {
		t.Fatalf("initCommon: %v", err)
	}
	t.Cleanup(shutdown)
	if dl != nil {
		t.Cleanup(func() { dl.Close() })
	}

	if tools.GetMCPToolRunner() != nil {
		t.Fatalf("connectMCP=false must not connect any MCP server")
	}
	var schema []map[string]any
	if err := json.Unmarshal(cfg.Schema, &schema); err != nil {
		t.Fatalf("unmarshal cfg.Schema: %v", err)
	}
	if schemaHasFunctionNamed(schema, "mcp_weather_forecast") {
		t.Fatalf("did not expect mcp_weather_forecast without connecting MCP")
	}
}

// TestInitCommon_NoMCPFlag_OverridesConnectMCPTrue covers the opt-out:
// --no-mcp must win even when the caller passes connectMCP=true (as run
// now does, for cron's sake — see initCommon's doc comment).
func TestInitCommon_NoMCPFlag_OverridesConnectMCPTrue(t *testing.T) {
	requireShForWiring(t)
	writeWiringTestHome(t)

	prov := &fakeProvider{name: "wiretest-prov-3", configured: true, models: []string{"m1"}}
	providers.Register(prov)

	restore := tools.SetMCPToolRunnerForTests(nil)
	t.Cleanup(func() {
		tools.ShutdownMCP()
		restore()
	})

	cmd := newInitCommonTestCmd()
	if err := cmd.Flags().Set("model", "wiretest-prov-3/m1"); err != nil {
		t.Fatalf("set model flag: %v", err)
	}
	if err := cmd.Flags().Set("no-mcp", "true"); err != nil {
		t.Fatalf("set no-mcp flag: %v", err)
	}

	_, _, _, _, _, _, _, dl, shutdown, err := initCommon(cmd, true, false)
	if err != nil {
		t.Fatalf("initCommon: %v", err)
	}
	t.Cleanup(shutdown)
	if dl != nil {
		t.Cleanup(func() { dl.Close() })
	}

	if tools.GetMCPToolRunner() != nil {
		t.Fatalf("--no-mcp must override connectMCP=true")
	}
}

func schemaHasFunctionNamed(schema []map[string]any, name string) bool {
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
