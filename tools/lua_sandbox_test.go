package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/hooks"
)

// luaToolFromSource writes src as a Lua tool script and loads it with
// loadLuaTool, the same path LoadLuaTools takes for each file.
func luaToolFromSource(t *testing.T, src string) *LuaTool {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool.lua")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	tool, err := loadLuaTool(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return tool
}

// replaceRegisteredTool puts run in place of the real tool called name and
// returns a func that restores the real one. swapTool refuses names that are
// already registered, and bash is one of them.
func replaceRegisteredTool(t *testing.T, name string, run func(context.Context, map[string]any) ToolResult) func() {
	t.Helper()
	real, ok := lookupTool(name)
	if !ok {
		t.Fatalf("%q is not registered", name)
	}
	registerTool(name, &fakeTool{name: name, run: run})
	return func() { registerTool(name, real) }
}

// ---------------------------------------------------------------------------
// The sandbox: a Lua tool cannot reach the machine without a tool
// ---------------------------------------------------------------------------

func TestLuaTool_OsExecuteIsBlocked(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	tool := luaToolFromSource(t, `return {schema = {name = "os-exec"}, run = function(ctx, args)
		os.execute("touch " .. args.marker)
		return {success = true, content = "ran"}
	end}`)

	res := tool.Run(context.Background(), map[string]any{"marker": marker})
	if res.Success {
		t.Fatalf("os.execute ran from a lua tool: %q", res.Content)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran and created the marker file")
	}
}

func TestLuaTool_IoOpenIsBlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "written.txt")
	tool := luaToolFromSource(t, `return {schema = {name = "io-open"}, run = function(ctx, args)
		local f = io.open(args.path, "w")
		f:write("x")
		f:close()
		return {success = true, content = "wrote"}
	end}`)

	res := tool.Run(context.Background(), map[string]any{"path": path})
	if res.Success {
		t.Fatalf("io.open worked inside a lua tool: %q", res.Content)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the file was written around the tool layer")
	}
}

// TestLuaTool_TopLevelCodeIsSandboxed: the top-level chunk runs at load time,
// before any run call, so it must not have the full standard library either.
func TestLuaTool_TopLevelCodeIsSandboxed(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	path := filepath.Join(t.TempDir(), "tool.lua")
	src := `os.execute("touch ` + marker + `")
return {schema = {name = "top-level"}, run = function(ctx, args) return {success = true} end}`
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadLuaTool(path); err == nil {
		t.Fatal("top-level code loaded with os.execute available")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("top-level code ran a command at load time")
	}
}

// ---------------------------------------------------------------------------
// ctx.* goes through RunTool, so the tool layer applies to it
// ---------------------------------------------------------------------------

func TestLuaTool_CtxRunGoesThroughRunTool(t *testing.T) {
	var gotCommand string
	defer replaceRegisteredTool(t, "bash", func(_ context.Context, input map[string]any) ToolResult {
		gotCommand, _ = input["command"].(string)
		return ToolResult{Type: "result", Success: true, Content: "from fake bash\n"}
	})()
	tool := luaToolFromSource(t, `return {schema = {name = "run-bash"}, run = function(ctx, args)
		return {success = true, content = ctx.run("echo hi")}
	end}`)

	res := tool.Run(context.Background(), map[string]any{})
	if !res.Success {
		t.Fatalf("tool failed: %s", res.Error)
	}
	if gotCommand != "echo hi" {
		t.Fatalf("ctx.run did not reach the bash tool: command %q", gotCommand)
	}
	if res.Content != "from fake bash" {
		t.Fatalf("ctx.run should return the trimmed stdout, got %q", res.Content)
	}

	// The same call under a tool gate that does not allow bash: the fake
	// must not run, and the script gets the gate's refusal as text.
	gotCommand = ""
	gated := WithToolGate(context.Background(), AllowOnly("read"))
	res = tool.Run(gated, map[string]any{})
	if !res.Success {
		t.Fatalf("tool failed: %s", res.Error)
	}
	if gotCommand != "" {
		t.Fatalf("ctx.run reached bash under a gate that denies it: command %q", gotCommand)
	}
	if res.Content == "from fake bash" || res.Content == "" {
		t.Fatalf("ctx.run should return the gate error, got %q", res.Content)
	}
}

// TestLuaTool_CtxReadReturnsNotice: a cut file gives its text as the first
// value and the read tool's notice as a second one, so the script sees two
// values and the notice is not file text.
func TestLuaTool_CtxReadReturnsNotice(t *testing.T) {
	long := filepath.Join(t.TempDir(), "long.txt")
	if err := os.WriteFile(long, []byte(strings.Repeat("line\n", 2500)), 0644); err != nil {
		t.Fatal(err)
	}
	tool := luaToolFromSource(t, `return {schema = {name = "read-notice"}, run = function(ctx, args)
		local text, notice = ctx.read(args.path)
		local n = select("#", ctx.read(args.path))
		return {success = true, content = n .. "|" .. tostring(notice) .. "|" .. tostring(string.find(text, "Showing lines", 1, true))}
	end}`)

	res := tool.Run(context.Background(), map[string]any{"path": long})
	if !res.Success {
		t.Fatalf("tool failed: %s", res.Error)
	}
	if !strings.HasPrefix(res.Content, "2|[Showing lines ") {
		t.Fatalf("a cut file should return two values, the second the notice: %q", res.Content)
	}
	if !strings.HasSuffix(res.Content, "|nil") {
		t.Fatalf("the notice should not stay in the file text: %q", res.Content)
	}
}

// TestLuaTool_CtxReadFirstLineTooLong: when the first line alone is over the
// byte cap, the read tool returns only its notice, so the text is empty.
func TestLuaTool_CtxReadFirstLineTooLong(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wide.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 60*1024)+"\nsecond\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := luaToolFromSource(t, `return {schema = {name = "read-wide"}, run = function(ctx, args)
		local text, notice = ctx.read(args.path)
		return {success = true, content = "[" .. text .. "]" .. tostring(notice)}
	end}`)

	res := tool.Run(context.Background(), map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("tool failed: %s", res.Error)
	}
	if !strings.HasPrefix(res.Content, "[][Line 1 ") {
		t.Fatalf("the notice should come back as the second value and the text should be empty: %q", res.Content)
	}
}

// TestLuaTool_CtxReadWholeFileKeepsItsText: a file that the read tool does not
// cut, but whose text contains both notice wordings, is returned whole with
// one value. Matching the notices anywhere in the text would cut it.
func TestLuaTool_CtxReadWholeFileKeepsItsText(t *testing.T) {
	body := "alpha\nNo partial line returned.\n\n[Showing lines 1-2 of 9]\nomega\n"
	path := filepath.Join(t.TempDir(), "transcript.txt")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	tool := luaToolFromSource(t, `return {schema = {name = "read-whole"}, run = function(ctx, args)
		local text, notice = ctx.read(args.path)
		return {success = true, content = select("#", ctx.read(args.path)) .. "|" .. tostring(notice) .. "|" .. text}
	end}`)

	res := tool.Run(context.Background(), map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("tool failed: %s", res.Error)
	}
	if want := "1|nil|" + body; res.Content != want {
		t.Fatalf("got %q, want %q", res.Content, want)
	}
}

// TestLuaTool_CtxReadWholeFileHasOneValue: a file the read tool does not cut
// gives one value, as before.
func TestLuaTool_CtxReadWholeFileHasOneValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := luaToolFromSource(t, `return {schema = {name = "read-short"}, run = function(ctx, args)
		return {success = true, content = select("#", ctx.read(args.path)) .. "|" .. ctx.read(args.path)}
	end}`)

	res := tool.Run(context.Background(), map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("tool failed: %s", res.Error)
	}
	if res.Content != "1|hello\n" {
		t.Fatalf("got %q, want %q", res.Content, "1|hello\n")
	}
}

func TestLuaTool_CtxWriteHonoursPreToolHook(t *testing.T) {
	defer hooks.SetForTesting([]hooks.Hook{{
		Event: hooks.EventPreTool, Tools: []string{"write"},
		Command: `echo protected; exit 1`,
	}})()
	path := filepath.Join(t.TempDir(), "x.txt")
	tool := luaToolFromSource(t, `return {schema = {name = "write-hook"}, run = function(ctx, args)
		local ok = ctx.write(args.path, "data")
		return {success = true, content = tostring(ok)}
	end}`)

	res := tool.Run(context.Background(), map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("tool failed: %s", res.Error)
	}
	if res.Content != "false" {
		t.Fatalf("ctx.write returned %q, want false", res.Content)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the pre_tool hook did not stop ctx.write")
	}
}

func TestLuaTool_CtxWriteHonoursToolGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.txt")
	tool := luaToolFromSource(t, `return {schema = {name = "write-gate"}, run = function(ctx, args)
		local ok = ctx.write(args.path, "data")
		return {success = true, content = tostring(ok)}
	end}`)

	ctx := WithToolGate(context.Background(), AllowOnly("read"))
	res := tool.Run(ctx, map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("tool failed: %s", res.Error)
	}
	if res.Content != "false" {
		t.Fatalf("ctx.write returned %q, want false", res.Content)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the tool gate did not stop ctx.write")
	}
}

// ---------------------------------------------------------------------------
// Cancel reaches pure Lua code, not only tool calls
// ---------------------------------------------------------------------------

func TestLuaTool_CancelStopsInfiniteLoop(t *testing.T) {
	tool := luaToolFromSource(t, `return {schema = {name = "spin"}, run = function(ctx, args)
		while true do end
	end}`)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ToolResult, 1)
	go func() { done <- tool.Run(ctx, map[string]any{}) }()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if res.Success {
			t.Fatalf("a cancelled loop reported success: %q", res.Content)
		}
		if !strings.Contains(res.Error, "cancel") {
			t.Errorf("error should name the cancel: %q", res.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the tool still runs 2 seconds after its context was cancelled")
	}
}
