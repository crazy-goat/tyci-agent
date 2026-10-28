package tools

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// createSource is a valid Lua tool script that echoes its input.
const createSource = `return {
  schema = {
    name = "made_echo",
    description = "Echoes its input",
    parameters = {
      input = {type = "string", description = "Text to echo"}
    }
  },
  run = function(ctx, args)
    return {success = true, content = "echo: " .. (args.input or "none")}
  end
}`

// createInRun calls tool_create with name and source in a context whose run folder is dir.
func createInRun(t *testing.T, dir, name, source string) ToolResult {
	t.Helper()
	ctx := WithRunDir(context.Background(), dir)
	return RunTool(ctx, "tool_create", map[string]any{"name": name, "source": source})
}

func registered(name string) bool {
	_, ok := lookupTool(name)
	return ok
}

func TestToolCreateRegistersAndRuns(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })

	res := createInRun(t, dir, "made_echo", createSource)
	if !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "tools", "made_echo.lua")); err != nil {
		t.Fatalf("the script was not written into the run folder: %v", err)
	}
	if !registered("made_echo") {
		t.Fatal("the new tool is not in the registry")
	}

	out := RunTool(context.Background(), "made_echo", map[string]any{"input": "hi"})
	if !out.Success || out.Content != "echo: hi" {
		t.Fatalf("the new tool did not run: success=%v content=%q error=%q", out.Success, out.Content, out.Error)
	}
}

func TestToolCreateBadLuaRegistersNothing(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })

	cases := map[string]string{
		"syntax error":     `return {`,
		"no run function":  `return {schema = {name = "bad_one"}}`,
		"wrong name":       `return {schema = {name = "other_name"}, run = function(ctx, args) return {success = true} end}`,
		"no schema at all": `return {run = function(ctx, args) return {success = true} end}`,
	}
	for label, source := range cases {
		t.Run(label, func(t *testing.T) {
			res := createInRun(t, dir, "bad_one", source)
			if res.Success {
				t.Fatal("tool_create accepted a script that does not load")
			}
			if registered("bad_one") {
				t.Fatal("a failed tool_create left a tool in the registry")
			}
			if _, err := os.Stat(filepath.Join(dir, "tools", "bad_one.lua")); !os.IsNotExist(err) {
				t.Fatalf("a failed tool_create left its file behind: %v", err)
			}
		})
	}
}

func TestToolCreateRefusesBadNames(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()

	// "read" is a builtin, "mcp_x_y" is an MCP name, "../x" would leave the run folder.
	for _, name := range []string{"read", "mcp_x_y", "../x", "Bad-Name", ""} {
		res := createInRun(t, dir, name, createSource)
		if res.Success {
			t.Fatalf("tool_create accepted the name %q", name)
		}
	}
	if !registered("read") {
		t.Fatal("the builtin read tool was replaced")
	}
	if _, err := os.Stat(filepath.Join(dir, "tools")); !os.IsNotExist(err) {
		t.Fatalf("a refused name still wrote a tools folder: %v", err)
	}
}

func TestToolCreateRefusesExistingTool(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })

	if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
		t.Fatalf("first tool_create failed: %s", res.Error)
	}
	if res := createInRun(t, dir, "made_echo", createSource); res.Success {
		t.Fatal("a second tool_create with the same name succeeded")
	}
}

func TestToolCreateNeedsRunDir(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()

	res := RunTool(context.Background(), "tool_create", map[string]any{"name": "made_echo", "source": createSource})
	if res.Success {
		t.Fatal("tool_create worked without a run folder")
	}
	if res.Error == "" {
		t.Fatal("the refusal has no reason")
	}
	if registered("made_echo") {
		t.Fatal("tool_create without a run folder registered a tool")
	}
}

func TestToolCreateRemovedWhenRunEnds(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	other := t.TempDir()
	t.Cleanup(func() {
		RemoveRunTools(dir)
		RemoveRunTools(other)
	})

	if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}
	if res := createInRun(t, other, "other_echo", replaceName(createSource, "other_echo")); !res.Success {
		t.Fatalf("tool_create in the other run failed: %s", res.Error)
	}

	RemoveRunTools(dir)
	if registered("made_echo") {
		t.Fatal("the tool of the ended run is still registered")
	}
	if !registered("other_echo") {
		t.Fatal("ending one run removed the tool of another run")
	}
}

func TestToolCreateRoleNeedsListing(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })

	// Full mode with no list: the mode grants everything except tool_create.
	if AllowedFor(Policy{Role: "r", Permission: PermissionFull}, toolCreateName) {
		t.Fatal("a full role without a tools list may call tool_create")
	}
	// Worktree-write with a list that does not name it: refused.
	if AllowedFor(Policy{Role: "r", Permission: PermissionWorktreeWrite, Tools: []string{"read"}}, toolCreateName) {
		t.Fatal("a role whose list does not name tool_create may call it")
	}
	// The list names it: allowed, even with worktree-write.
	if !AllowedFor(Policy{Role: "r", Permission: PermissionWorktreeWrite, Tools: []string{"read", toolCreateName}}, toolCreateName) {
		t.Fatal("a role whose list names tool_create may not call it")
	}

	// End to end: a role without it in its list is refused and registers nothing.
	ctx := WithPolicy(WithRunDir(context.Background(), dir), Policy{Role: "r", Permission: PermissionWorktreeWrite, Tools: []string{"read"}, Dir: dir})
	res := RunTool(ctx, "tool_create", map[string]any{"name": "made_echo", "source": createSource})
	if res.Success {
		t.Fatal("a role without tool_create in its list created a tool")
	}
	if registered("made_echo") {
		t.Fatal("a refused tool_create registered a tool")
	}
}

func TestToolCreateSchemaVisibility(t *testing.T) {
	names := func(schema []map[string]any) []string {
		var out []string
		for _, s := range schema {
			if fn, ok := s["function"].(map[string]any); ok {
				if n, ok := fn["name"].(string); ok {
					out = append(out, n)
				}
			}
		}
		return out
	}

	if !slices.Contains(names(GetTopLevelToolsSchema()), toolCreateName) {
		t.Fatal("the main chat schema does not offer tool_create")
	}
	if slices.Contains(names(GetSubagentToolsSchema()), toolCreateName) {
		t.Fatal("an unrestricted subagent is offered tool_create")
	}
	if !slices.Contains(names(subagentToolsSchemaFor([]string{"read", toolCreateName})), toolCreateName) {
		t.Fatal("a subagent whose list names tool_create is not offered it")
	}
	if slices.Contains(names(subagentToolsSchemaFor([]string{"read"})), toolCreateName) {
		t.Fatal("a subagent whose list does not name tool_create is offered it")
	}
}

func TestSessionFolderRemovedAtSessionEnd(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()

	dir, end, err := NewSessionFolder()
	if err != nil {
		t.Fatalf("NewSessionFolder: %v", err)
	}
	if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
		t.Fatalf("tool_create in the session failed: %s", res.Error)
	}

	end()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the session folder survived the session end: %v", err)
	}
	if registered("made_echo") {
		t.Fatal("the session tool survived the session end")
	}
}

// replaceName returns createSource with its schema name changed to name.
func replaceName(source, name string) string {
	return strings.Replace(source, `name = "made_echo"`, `name = "`+name+`"`, 1)
}

// subagentCtx is a context as a subagent's tool calls see it: the run folder
// is dir, and the subagent gate is installed the way main.go installs it.
func subagentCtx(dir string, allowed []string, policy *Policy) context.Context {
	ctx := WithRunDir(context.Background(), dir)
	if policy != nil {
		ctx = WithPolicy(ctx, *policy)
	}
	return WithToolGate(ctx, SubagentToolCreateGate(ctx, allowed))
}

func TestToolCreateRefusedForUnrestrictedSubagent(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })

	res := RunTool(subagentCtx(dir, nil, nil), "tool_create", map[string]any{"name": "made_echo", "source": createSource})
	if res.Success {
		t.Fatal("an unrestricted subagent created a tool")
	}
	if registered("made_echo") {
		t.Fatal("a refused subagent tool_create registered a tool")
	}
}

func TestToolCreateAllowedForListedRole(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })

	p := Policy{Role: "r", Permission: PermissionWorktreeWrite, Tools: []string{"read", toolCreateName}, Dir: dir}
	res := RunTool(subagentCtx(dir, p.Tools, &p), "tool_create", map[string]any{"name": "made_echo", "source": createSource})
	if !res.Success {
		t.Fatalf("a role whose list names tool_create could not create a tool: %s", res.Error)
	}
	if !registered("made_echo") {
		t.Fatal("the listed role's tool is not registered")
	}
}

func TestToolCreateRefusedFromLuaInSubagent(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })

	source := replaceName(createSource, "lua_made")
	script := `return tool("tool_create", {name = "lua_made", source = [==[` + source + `]==]})`

	// The name matches the source, so only the gate can refuse the call.
	res := RunTool(subagentCtx(dir, nil, nil), "lua", map[string]any{"script": script})
	if registered("lua_made") {
		t.Fatal("an unrestricted subagent created a tool through lua tool()")
	}
	if !strings.Contains(res.Error+res.Content, "is not available to this subagent") {
		t.Fatalf("the refusal is not the gate error: success=%v content=%q error=%q", res.Success, res.Content, res.Error)
	}

	// Control: the same script from a role that lists tool_create is created.
	p := Policy{Role: "r", Permission: PermissionWorktreeWrite, Tools: []string{"lua", toolCreateName}, Dir: dir}
	RunTool(subagentCtx(dir, p.Tools, &p), "lua", map[string]any{"script": script})
	if !registered("lua_made") {
		t.Fatal("control: a role that lists tool_create did not create the tool through lua tool()")
	}
}

// noTempLeft fails when dir/tools still has a temp file or the named tool file.
func noTempLeft(t *testing.T, dir, name string) {
	t.Helper()
	if matches, _ := filepath.Glob(filepath.Join(dir, "tools", "*.tmp")); len(matches) > 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
	if _, err := os.Stat(filepath.Join(dir, "tools", name+".lua")); err == nil {
		t.Fatalf("the script of %q was kept", name)
	}
}

func TestToolCreateLoadIsBounded(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })

	source := "while true do end\n" + createSource
	ctx, cancel := context.WithTimeout(WithRunDir(context.Background(), dir), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := RunTool(ctx, "tool_create", map[string]any{"name": "made_echo", "source": source})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("a script that never returns held the call for %v", elapsed)
	}
	if res.Success {
		t.Fatal("a script that never returns was accepted")
	}
	if registered("made_echo") {
		t.Fatal("a script that never returns was registered")
	}
	noTempLeft(t, dir, "made_echo")
}

func TestToolCreatePassingTestsRegister(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })

	ctx := WithRunDir(context.Background(), dir)
	tests := []any{map[string]any{"args": map[string]any{"input": "hi"}, "expect": "echo: hi"}}
	res := RunTool(ctx, "tool_create", map[string]any{"name": "made_echo", "source": createSource, "tests": tests})
	if !res.Success || !registered("made_echo") {
		t.Fatalf("a passing test blocked the tool: success=%v error=%q", res.Success, res.Error)
	}
	noTempLeft(t, dir, "")
}

func TestToolCreateFailingTestDeletesFile(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })

	ctx := WithRunDir(context.Background(), dir)
	tests := []any{map[string]any{"args": map[string]any{"input": "hi"}, "expect": "not in the result"}}
	res := RunTool(ctx, "tool_create", map[string]any{"name": "made_echo", "source": createSource, "tests": tests})
	if res.Success {
		t.Fatal("a failing test was accepted")
	}
	if registered("made_echo") {
		t.Fatal("a failing test left the tool registered")
	}
	noTempLeft(t, dir, "made_echo")
}

func TestToolCreateOsExecuteFailsInSandbox(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()

	t.Run("at load", func(t *testing.T) {
		dir := t.TempDir()
		t.Cleanup(func() { RemoveRunTools(dir) })
		source := "os.execute('true')\n" + createSource
		res := createInRun(t, dir, "made_echo", source)
		if res.Success || registered("made_echo") {
			t.Fatal("a script that calls os.execute at load was accepted")
		}
		noTempLeft(t, dir, "made_echo")
	})

	t.Run("in run", func(t *testing.T) {
		dir := t.TempDir()
		t.Cleanup(func() { RemoveRunTools(dir) })
		source := replaceName(strings.Replace(createSource,
			`content = "echo: " .. (args.input or "none")`,
			`os.execute('true'); return {success = true, content = "ran"}`, 1), "made_echo")
		ctx := WithRunDir(context.Background(), dir)
		tests := []any{map[string]any{"expect": "ran"}}
		res := RunTool(ctx, "tool_create", map[string]any{"name": "made_echo", "source": source, "tests": tests})
		if res.Success || registered("made_echo") {
			t.Fatal("a script that calls os.execute in run was accepted")
		}
		noTempLeft(t, dir, "made_echo")
	})
}
