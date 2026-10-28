package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadOnlyRefusesWrite(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkdir(WithPolicy(context.Background(), Policy{Role: "review", Permission: PermissionReadOnly, Network: true, Dir: dir}), dir)
	res := RunTool(ctx, "write", map[string]any{"path": "a.txt", "content": "x"})
	if res.Success {
		t.Fatal("read-only role wrote a file")
	}
	if !strings.Contains(res.Error, `tool "write" is not allowed for role "review" (permission read-only)`) {
		t.Fatalf("refusal = %q", res.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err == nil {
		t.Fatal("file was created")
	}
}

func TestReadOnlyRefusesBash(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkdir(WithPolicy(context.Background(), Policy{Role: "review", Permission: PermissionReadOnly, Network: true, Dir: dir}), dir)
	res := RunTool(ctx, "bash", map[string]any{"command": "true"})
	if res.Success || !strings.Contains(res.Error, `"bash" is not allowed`) {
		t.Fatalf("bash result = %+v", res)
	}
}

func TestReadOnlyAllowsRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkdir(WithPolicy(context.Background(), Policy{Role: "review", Permission: PermissionReadOnly, Network: true, Dir: dir}), dir)
	res := RunTool(ctx, "read", map[string]any{"path": "a.txt"})
	if !res.Success {
		t.Fatalf("read refused: %+v", res)
	}
}

func TestToolsCannotWidenMode(t *testing.T) {
	p := Policy{Permission: PermissionReadOnly, Network: true, Tools: []string{"read", "bash"}}
	if AllowedFor(p, "bash") {
		t.Fatal("listed bash allowed under read-only")
	}
	if !AllowedFor(p, "read") {
		t.Fatal("listed read refused")
	}
	if AllowedFor(p, "find") {
		t.Fatal("unlisted find allowed")
	}
}

func TestMCPNeedsListing(t *testing.T) {
	ro := Policy{Permission: PermissionReadOnly, Network: true}
	if AllowedFor(ro, "mcp_x_y") {
		t.Fatal("unlisted MCP tool allowed under read-only")
	}
	listed := Policy{Permission: PermissionWorktreeWrite, Network: true, Tools: []string{"read", "mcp_x_y"}}
	if !AllowedFor(listed, "mcp_x_y") {
		t.Fatal("MCP tool listed by name refused")
	}
	if AllowedFor(listed, "mcp_q_y") {
		t.Fatal("unlisted MCP tool allowed")
	}
}

// TestReadOnlyRefusesListedMCPTool: a read-only role cannot call an MCP tool,
// even when its tools list names it, directly or through lua tool().
func TestReadOnlyRefusesListedMCPTool(t *testing.T) {
	for _, list := range [][]string{{"read", "mcp_weather_forecast"}, {"read", "mcp_weather_*"}} {
		client := &recordingMCPClient{fakeMCPClient: fakeMCPClient{name: "weather"}}
		r := newTestRunnerWithTool("weather", "forecast")
		r.clients["weather"] = client
		withRunner(t, r)
		ctx := WithPolicy(context.Background(), Policy{Role: "review", Permission: PermissionReadOnly, Network: true, Tools: list, Dir: t.TempDir()})

		res := RunTool(ctx, "mcp_weather_forecast", map[string]any{})
		if res.Success || !strings.Contains(res.Error, `is not allowed for role "review"`) {
			t.Fatalf("tools %v: direct call result = %+v", list, res)
		}

		res = (&LuaEvalTool{}).Run(ctx, map[string]any{
			"script": `local r = tool("mcp_weather_forecast", {}); return r.success and "ran" or r.error`,
		})
		if !res.Success || !strings.Contains(res.Content, `is not allowed for role "review"`) {
			t.Fatalf("tools %v: lua call result = %+v", list, res)
		}
		if client.calls > 0 {
			t.Fatalf("tools %v: the MCP server was called", list)
		}
	}
}

// TestReadOnlyOffersNoMCPTool: the schema of a read-only role has no MCP tool.
func TestReadOnlyOffersNoMCPTool(t *testing.T) {
	withRunner(t, newTestRunnerWithTool("weather", "forecast"))
	for _, list := range [][]string{nil, {"read", "mcp_weather_forecast"}, {"mcp_weather_*"}} {
		p := Policy{Role: "review", Permission: PermissionReadOnly, Network: true, Tools: list}
		names := p.ToolNames()
		if slices.Contains(names, "mcp_weather_forecast") {
			t.Fatalf("tools %v: ToolNames offers the MCP tool: %v", list, names)
		}
		var schema []map[string]any
		if err := json.Unmarshal(GetSubagentToolsSchemaJSONFor(names), &schema); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if schemaHasTool(t, schema, "mcp_weather_forecast") {
			t.Fatalf("tools %v: the schema offers the MCP tool", list)
		}
	}
}

// TestUnsetOrReadOnlyModeRefusesListedMCPTool: an unset mode reads as read-only
// in modeAllows, so it gets no MCP tool either.
func TestUnsetOrReadOnlyModeRefusesListedMCPTool(t *testing.T) {
	for _, perm := range []string{"", PermissionReadOnly} {
		p := Policy{Permission: perm, Network: true, Tools: []string{"mcp_x_y"}}
		if AllowedFor(p, "mcp_x_y") {
			t.Fatalf("permission %q: listed MCP tool allowed", perm)
		}
	}
}

// TestReadOnlyLuaCannotWriteOrRunBash: a listed write or bash cannot widen a
// read-only mode, and a lua script reaches the same gate.
func TestReadOnlyLuaCannotWriteOrRunBash(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkdir(WithPolicy(context.Background(), Policy{Role: "review", Permission: PermissionReadOnly, Network: true, Tools: []string{"read", "write", "bash"}, Dir: dir}), dir)
	res := (&LuaEvalTool{}).Run(ctx, map[string]any{
		"script": `
			local w = tool("write", {path = "x.txt", content = "x"})
			local b = tool("bash", {command = "touch y.txt"})
			return (w.success and "write ran" or w.error) .. "\n" .. (b.success and "bash ran" or b.error)`,
	})
	if !res.Success {
		t.Fatalf("script failed: %s", res.Error)
	}
	if strings.Count(res.Content, `is not allowed for role "review" (permission read-only)`) != 2 {
		t.Fatalf("write or bash was not refused: %q", res.Content)
	}
	for _, name := range []string{"x.txt", "y.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Fatalf("%s was created", name)
		}
	}
}

func TestAlwaysAllowedToolsStayAllowed(t *testing.T) {
	p := Policy{Permission: PermissionReadOnly, Network: false, Tools: []string{"read"}}
	for _, name := range []string{"help", "lua", "report_progress"} {
		if !AllowedFor(p, name) {
			t.Fatalf("%s refused", name)
		}
	}
}

func TestWorktreeWriteRefusesOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkdir(WithPolicy(context.Background(), Policy{Role: "worker", Permission: PermissionWorktreeWrite, Network: true, Dir: root}), root)
	for _, path := range []string{"../x.txt", filepath.Join(outside, "x.txt"), "link/x.txt"} {
		res := RunTool(ctx, "write", map[string]any{"path": path, "content": "x"})
		if res.Success || !strings.Contains(res.Error, "outside the worktree") {
			t.Fatalf("write %q: %+v", path, res)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "x.txt")); err == nil {
		t.Fatal("file written outside the worktree")
	}
}

// Without a policy (a normal agent), write may use any absolute path.
func TestNoPolicyWriteAllowsOutside(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "x.txt")
	ctx := WithWorkdir(context.Background(), root)
	res := RunTool(ctx, "write", map[string]any{"path": outside, "content": "x"})
	if !res.Success {
		t.Fatalf("write outside refused without policy: %+v", res)
	}
}

func TestWorktreeWriteAllowsInside(t *testing.T) {
	root := t.TempDir()
	ctx := WithWorkdir(WithPolicy(context.Background(), Policy{Role: "worker", Permission: PermissionWorktreeWrite, Network: true, Dir: root}), root)
	res := RunTool(ctx, "write", map[string]any{"path": "x.txt", "content": "x"})
	if !res.Success {
		t.Fatalf("write inside refused: %+v", res)
	}
}

func TestNetworkFalseHidesWeb(t *testing.T) {
	p := Policy{Permission: PermissionFull, Network: false}
	names := p.ToolNames()
	if names == nil {
		t.Fatal("full without network must still restrict the list")
	}
	if slices.Contains(names, "web") {
		t.Fatal("web offered without network")
	}
	for _, n := range names {
		if isMCPTool(n) {
			t.Fatalf("MCP tool %s offered without network", n)
		}
	}
	if AllowedFor(p, "web") {
		t.Fatal("web allowed without network")
	}
}

func TestNetworkFalseKeepsListedMCP(t *testing.T) {
	p := Policy{Permission: PermissionWorktreeWrite, Network: false, Tools: []string{"read", "mcp_x_y"}}
	if !slices.Contains(p.ToolNames(), "mcp_x_y") {
		t.Fatalf("listed MCP tool dropped: %v", p.ToolNames())
	}
	if AllowedFor(Policy{Permission: PermissionWorktreeWrite, Network: false}, "mcp_x_y") {
		t.Fatal("unlisted MCP tool allowed without network")
	}
}

func TestToolNamesNeverEmptyWhenRestricted(t *testing.T) {
	names := Policy{Permission: PermissionReadOnly, Network: true, Tools: []string{"bash"}}.ToolNames()
	if len(names) == 0 {
		t.Fatal("empty list would lift the restriction")
	}
	if slices.Contains(names, "bash") {
		t.Fatal("bash offered under read-only")
	}
}

// AllowedFor and ToolNames must agree: an unlisted MCP tool is offered only
// in full mode with the network on.
func TestAllowedForMCPAgreesWithToolNames(t *testing.T) {
	const mcp = "mcp_x_y"
	for _, perm := range []string{PermissionReadOnly, PermissionWorktreeWrite, PermissionFull} {
		for _, network := range []bool{true, false} {
			p := Policy{Permission: perm, Network: network}
			names := p.ToolNames()
			offered := names == nil || slices.Contains(names, mcp)
			want := perm == PermissionFull && network
			if AllowedFor(p, mcp) != want || offered != want {
				t.Errorf("%s network=%v: AllowedFor=%v ToolNames offers=%v, want %v", perm, network, AllowedFor(p, mcp), offered, want)
			}
		}
	}
}
