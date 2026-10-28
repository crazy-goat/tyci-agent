package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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

	cases := map[string]struct{ source, reason string }{
		"syntax error":     {`return {`, "failed to parse"},
		"no run function":  {`return {schema = {name = "bad_one"}}`, "'run' function"},
		"wrong name":       {`return {schema = {name = "other_name"}, run = function(ctx, args) return {success = true} end}`, "schema.name is"},
		"no schema at all": {`return {run = function(ctx, args) return {success = true} end}`, "'schema' table"},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			res := createInRun(t, dir, "bad_one", c.source)
			if res.Success {
				t.Fatal("tool_create accepted a script that does not load")
			}
			if !strings.Contains(res.Error, c.reason) {
				t.Fatalf("the refusal is not the expected one (want %q): %q", c.reason, res.Error)
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
	// "read" is a valid shape, so it is refused as a clash with the builtin.
	reasons := map[string]string{
		"read": "already exists", "mcp_x_y": "is not valid", "../x": "is not valid",
		"Bad-Name": "is not valid", "": "is not valid",
	}
	for name, reason := range reasons {
		res := createInRun(t, dir, name, createSource)
		if res.Success {
			t.Fatalf("tool_create accepted the name %q", name)
		}
		if !strings.Contains(res.Error, reason) {
			t.Fatalf("the refusal of the name %q is not the expected one (want %q): %q", name, reason, res.Error)
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
	res := createInRun(t, dir, "made_echo", createSource)
	if res.Success {
		t.Fatal("a second tool_create with the same name succeeded")
	}
	if !strings.Contains(res.Error, "already exists") {
		t.Fatalf("the refusal is not the name clash: %q", res.Error)
	}
}

func TestToolCreateNeedsRunDir(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()

	res := RunTool(context.Background(), "tool_create", map[string]any{"name": "made_echo", "source": createSource})
	if res.Success {
		t.Fatal("tool_create worked without a run folder")
	}
	if !strings.Contains(res.Error, "run folder") {
		t.Fatalf("the refusal is not the missing run folder: %q", res.Error)
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
	if !strings.Contains(res.Error, "tool_create") {
		t.Fatalf("the refusal does not name tool_create: %q", res.Error)
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
	if !strings.Contains(res.Error, "is not available to this subagent") {
		t.Fatalf("the refusal is not the gate error: %q", res.Error)
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
	if !strings.Contains(res.Error, "does not load") {
		t.Fatalf("the refusal is not the load limit: %q", res.Error)
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
	if !strings.Contains(res.Error, "a test of the Lua tool failed") || !strings.Contains(res.Error, "does not contain") {
		t.Fatalf("the refusal is not the failing test: %q", res.Error)
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
		if !strings.Contains(res.Error, "does not load") || !strings.Contains(res.Error, "non-function") {
			t.Fatalf("the refusal is not the sandbox at load: %q", res.Error)
		}
		noTempLeft(t, dir, "made_echo")
	})

	t.Run("in run", func(t *testing.T) {
		dir := t.TempDir()
		t.Cleanup(func() { RemoveRunTools(dir) })
		source := replaceName(strings.Replace(createSource,
			`return {success = true, content = "echo: " .. (args.input or "none")}`,
			`os.execute('true'); return {success = true, content = "ran"}`, 1), "made_echo")
		ctx := WithRunDir(context.Background(), dir)
		tests := []any{map[string]any{"expect": "ran"}}
		res := RunTool(ctx, "tool_create", map[string]any{"name": "made_echo", "source": source, "tests": tests})
		if res.Success || registered("made_echo") {
			t.Fatal("a script that calls os.execute in run was accepted")
		}
		// The script must parse and load, so the test run is what refuses it.
		// os.execute is nil in the sandbox, so the call fails inside the test run.
		if !strings.Contains(res.Error, "a test of the Lua tool failed") || !strings.Contains(res.Error, "non-function") {
			t.Fatalf("the refusal is not the sandbox: %q", res.Error)
		}
		noTempLeft(t, dir, "made_echo")
	})
}

// writeRunTool writes source as the file name.lua in the tools folder of dir.
func writeRunTool(t *testing.T, dir, name, source string) {
	t.Helper()
	toolsDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, name+".lua"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
}

// After a restart the run folder keeps the tool files, and ReloadRunTools
// registers the good ones again. A bad file is skipped and the others load.
func TestReloadRunToolsRegistersGoodFilesOnly(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })
	writeRunTool(t, dir, "made_echo", createSource)
	writeRunTool(t, dir, "Bad-Name", replaceName(createSource, "Bad-Name"))
	writeRunTool(t, dir, "made_other", replaceName(createSource, "made_renamed"))
	writeRunTool(t, dir, "made_broken", "return {")

	ReloadRunTools(dir)

	if !registered("made_echo") {
		t.Fatal("the good tool was not registered again")
	}
	for _, name := range []string{"Bad-Name", "made_other", "made_broken"} {
		if registered(name) {
			t.Fatalf("the bad file %s was registered", name)
		}
	}
	if out := RunTool(context.Background(), "made_echo", map[string]any{"input": "hi"}); !out.Success || out.Content != "echo: hi" {
		t.Fatalf("the reloaded tool did not run: %+v", out)
	}
}

// A second reload of the same folder replaces the tools of that folder and
// does not report them as name conflicts.
func TestReloadRunToolsTwiceKeepsOneTool(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })
	writeRunTool(t, dir, "made_echo", createSource)

	ReloadRunTools(dir)
	ReloadRunTools(dir)
	if !registered("made_echo") {
		t.Fatal("the tool is gone after the second reload")
	}
	RemoveRunTools(dir)
	if registered("made_echo") {
		t.Fatal("the tool of the run survived RemoveRunTools")
	}
}

// A run folder without a tools folder is left alone.
func TestReloadRunToolsNoFolderIsNoop(t *testing.T) {
	ReloadRunTools(t.TempDir())
}

// startedAgo returns a folder under root with the name and a modification time d ago.
func startedAgo(t *testing.T, root, name string, d time.Duration) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-d)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A session folder whose owner PID is gone is removed. A folder of a live PID,
// a young folder without PID, an unrelated folder and a file are kept. An old
// folder without PID is removed.
func TestRemoveStaleSessionFolders(t *testing.T) {
	root := t.TempDir()

	// A child that has exited: its PID is free again.
	child := exec.Command("true")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	deadPID := child.Process.Pid

	dead := startedAgo(t, root, fmt.Sprintf("%s%d-x", sessionFolderPrefix, deadPID), 0)
	live := startedAgo(t, root, fmt.Sprintf("%s%d-x", sessionFolderPrefix, os.Getpid()), 0)
	young := startedAgo(t, root, sessionFolderPrefix+"x", time.Hour)
	old := startedAgo(t, root, sessionFolderPrefix+"y", 25*time.Hour)
	other := startedAgo(t, root, "other-"+strconv.Itoa(deadPID), 0)
	file := filepath.Join(root, fmt.Sprintf("%s%d-f", sessionFolderPrefix, deadPID))
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if n := RemoveStaleSessionFolders(root); n != 2 {
		t.Fatalf("removed %d folders, want 2", n)
	}
	for path, want := range map[string]bool{dead: false, old: false, live: true, young: true, other: true, file: true} {
		_, err := os.Stat(path)
		if exists := err == nil; exists != want {
			t.Errorf("%s exists = %v, want %v", filepath.Base(path), exists, want)
		}
	}
}

// NewSessionFolder names the folder with the PID of this process, so a later
// start can tell that the owner is alive.
func TestSessionFolderNameHoldsOwnPID(t *testing.T) {
	dir, end, err := NewSessionFolder()
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	want := fmt.Sprintf("%s%d-", sessionFolderPrefix, os.Getpid())
	if !strings.HasPrefix(filepath.Base(dir), want) {
		t.Fatalf("session folder %q does not start with %q", filepath.Base(dir), want)
	}
}
