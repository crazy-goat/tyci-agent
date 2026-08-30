package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/decodo/tyci/tools"
)

// TestWorkflowRunCLI_ConnectsMCP is round 1 finding 4's regression test:
// the reviewer manually flipped connectMCP from true to false at
// workflowcmd.go's setupProjectLocalEnv call site (RunE) and the whole `.`
// package's test suite still passed — F28's MCP third had no coverage at
// all, unlike hooks and Lua tools (workflowcmd_test.go's
// TestWorkflowRunCLI_TrustedProject_LoadsProjectLocalHooksAndLuaTools and
// its untrusted sibling), which are covered both ways.
//
// Harness shape copied from commands_mcp_wiring_test.go's
// TestInitCommon_ConnectMCPTrue_ConnectsAndAdvertisesServerTools: the fake
// server only answers "initialize" and "tools/list" (see
// mcpFakeServerScript's own doc comment), not an actual tool invocation, so
// this checks the same thing that test does — the server actually
// connected and its tool is registered — rather than driving the workflow
// script to call it (which would hang waiting for a "tools/call" response
// the fake server never sends).
func TestWorkflowRunCLI_ConnectsMCP(t *testing.T) {
	requireShForWiring(t)
	writeWiringTestHome(t)

	restore := tools.SetMCPToolRunnerForTests(nil)
	t.Cleanup(func() {
		tools.ShutdownMCP()
		restore()
	})

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "mcp_probe.lua")
	if err := os.WriteFile(scriptPath, []byte(`return "done"`), 0644); err != nil {
		t.Fatal(err)
	}

	cmd, _, _ := newWorkflowRunTestCmd(t)
	if err := cmd.Flags().Set("no-mcp", "false"); err != nil {
		t.Fatalf("set no-mcp flag: %v", err)
	}

	if err := workflowRunCmd.RunE(cmd, []string{scriptPath}); err != nil {
		t.Fatalf("workflowRunCmd.RunE: %v", err)
	}

	runner := tools.GetMCPToolRunner()
	if runner == nil {
		t.Fatalf("expected tyci workflow run (connectMCP=true, --no-mcp=false) to have connected the configured mcp.json server")
	}
	if !runner.HasTool("mcp_weather_forecast") {
		t.Fatalf("expected the configured weather server's forecast tool to be connected")
	}
}

// TestWorkflowRunCLI_NoMCPFlag_SkipsMCP is the companion negative case:
// --no-mcp must still win for `tyci workflow run`, exactly as it does for
// `tyci run` (initCommon's own TestInitCommon_NoMCPFlag_OverridesConnectMCPTrue).
func TestWorkflowRunCLI_NoMCPFlag_SkipsMCP(t *testing.T) {
	requireShForWiring(t)
	writeWiringTestHome(t)

	restore := tools.SetMCPToolRunnerForTests(nil)
	t.Cleanup(func() {
		tools.ShutdownMCP()
		restore()
	})

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "mcp_probe_off.lua")
	if err := os.WriteFile(scriptPath, []byte(`return "done"`), 0644); err != nil {
		t.Fatal(err)
	}

	// newWorkflowRunTestCmd's --no-mcp default is true — left as-is here.
	cmd, _, _ := newWorkflowRunTestCmd(t)

	if err := workflowRunCmd.RunE(cmd, []string{scriptPath}); err != nil {
		t.Fatalf("workflowRunCmd.RunE: %v", err)
	}

	if tools.GetMCPToolRunner() != nil {
		t.Fatalf("--no-mcp must prevent tyci workflow run from connecting any MCP server")
	}
}
