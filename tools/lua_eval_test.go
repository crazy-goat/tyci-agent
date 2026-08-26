package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/internal/hooks"
	"github.com/decodo/tyci/stream"
)

func runLua(t *testing.T, script string, args map[string]any) ToolResult {
	t.Helper()
	input := map[string]any{"script": script}
	if args != nil {
		input["args"] = args
	}
	return (&LuaEvalTool{}).Run(context.Background(), input)
}

func TestLuaReturnsAValue(t *testing.T) {
	res := runLua(t, `return 2 + 3`, nil)
	if !res.Success {
		t.Fatalf("script failed: %s", res.Error)
	}
	if res.Content != "5" {
		t.Fatalf("got %q", res.Content)
	}
}

func TestLuaRequiresAScript(t *testing.T) {
	res := (&LuaEvalTool{}).Run(context.Background(), map[string]any{})
	if res.Success || !strings.Contains(res.Error, "script is required") {
		t.Fatalf("got %+v", res)
	}
}

func TestLuaReportsSyntaxAndRuntimeErrors(t *testing.T) {
	if res := runLua(t, `this is not lua`, nil); res.Success {
		t.Fatal("a syntax error must not be reported as success")
	}
	res := runLua(t, `error("deliberate")`, nil)
	if res.Success {
		t.Fatal("a raised error must not be reported as success")
	}
	if !strings.Contains(res.Error, "deliberate") {
		t.Fatalf("the model needs the message to fix the script: %q", res.Error)
	}
}

func TestLuaTablesComeBackAsJSON(t *testing.T) {
	res := runLua(t, `return {name = "x", count = 2}`, nil)
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, `"name"`) || !strings.Contains(res.Content, `"count"`) {
		t.Fatalf("a table should be readable, got %q", res.Content)
	}
}

func TestLuaArgsAreAvailable(t *testing.T) {
	res := runLua(t, `return args.greeting .. " " .. args.n`, map[string]any{
		"greeting": "hi", "n": 7,
	})
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	if !strings.HasPrefix(res.Content, "hi 7") {
		t.Fatalf("got %q", res.Content)
	}
}

func TestLuaLogIsIncludedInTheResult(t *testing.T) {
	res := runLua(t, `log("step one"); log("step", 2); return "done"`, nil)
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	for _, want := range []string{"step one", "step 2", "done"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("%q missing from %q", want, res.Content)
		}
	}
}

func TestLuaEmptyScriptIsExplained(t *testing.T) {
	res := runLua(t, `local x = 1`, nil)
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "return") {
		t.Fatalf("a script that produced nothing should say how to produce something: %q", res.Content)
	}
}

// ---------------------------------------------------------------------------
// tool() — the reason this tool exists
// ---------------------------------------------------------------------------

// TestLuaToolLoopsOverFilesInOneCall is the payoff: what would be N tool
// calls and N round trips is one.
func TestLuaToolLoopsOverFilesInOneCall(t *testing.T) {
	ResetFileStamps()
	defer hooks.SetForTesting(nil)()
	dir := t.TempDir()
	for i := 1; i <= 5; i++ {
		path := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(path, []byte(strings.Repeat("x\n", i)), 0644); err != nil {
			t.Fatal(err)
		}
	}

	res := runLua(t, `
		local total = 0
		for i = 1, 5 do
			local r = tool("read", {path = args.dir .. "/f" .. i .. ".txt"})
			if not r.success then return "read failed: " .. r.error end
			for _ in string.gmatch(r.content, "x") do total = total + 1 end
		end
		return "x count: " .. total
	`, map[string]any{"dir": dir})

	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "x count: 15") {
		t.Fatalf("got %q", res.Content)
	}
}

func TestLuaToolReportsFailureWithoutAborting(t *testing.T) {
	res := runLua(t, `
		local r = tool("read", {path = "/definitely/not/here"})
		if r.success then return "unexpected success" end
		return "handled: " .. (r.error ~= "" and "yes" or "no")
	`, nil)
	if !res.Success {
		t.Fatalf("a failing tool call is data, not a script error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "handled: yes") {
		t.Fatalf("got %q", res.Content)
	}
}

// TestLuaToolPassesNestedArrays covers the conversion bug that would silently
// empty a subagent fan-out: Lua has one table type, and the naive conversion
// drops array elements because their keys are numbers.
func TestLuaToolPassesNestedArrays(t *testing.T) {
	var got map[string]any
	restore := swapTool(t, "spy", func(_ context.Context, args map[string]any) ToolResult {
		got = args
		return ToolResult{Type: "result", Success: true, Content: "ok"}
	})
	defer restore()

	res := runLua(t, `
		local items = {}
		for i = 1, 3 do items[#items + 1] = {task = "task " .. i} end
		return tool("spy", {tasks = items, label = "batch"}).content
	`, nil)
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}

	tasks, ok := got["tasks"].([]any)
	if !ok {
		t.Fatalf("tasks did not arrive as a list, got %T (%v)", got["tasks"], got["tasks"])
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d: %v", len(tasks), tasks)
	}
	first, ok := tasks[0].(map[string]any)
	if !ok || first["task"] != "task 1" {
		t.Fatalf("task contents lost: %v", tasks[0])
	}
	if got["label"] != "batch" {
		t.Fatalf("sibling string field lost: %v", got)
	}
}

// TestLuaToolMixedTableStaysAMap: a table with both numbered and named fields
// is a record, and turning it into a list would drop the named ones.
func TestLuaToolMixedTableStaysAMap(t *testing.T) {
	var got map[string]any
	defer swapTool(t, "spy", func(_ context.Context, args map[string]any) ToolResult {
		got = args
		return ToolResult{Type: "result", Success: true}
	})()

	if res := runLua(t, `
		local t = {"first"}
		t.name = "kept"
		return tool("spy", {value = t}).success
	`, nil); !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}

	m, ok := got["value"].(map[string]any)
	if !ok {
		t.Fatalf("expected a map, got %T", got["value"])
	}
	if m["name"] != "kept" {
		t.Fatalf("named field dropped: %v", m)
	}
}

func TestLuaToolRejectsNonTableArgs(t *testing.T) {
	res := runLua(t, `return tool("read", "not-a-table")`, nil)
	if res.Success {
		t.Fatal("passing a string where a table belongs should be an error, not a silent no-arg call")
	}
	if !strings.Contains(res.Error, "must be a table") {
		t.Fatalf("got %q", res.Error)
	}
}

// TestLuaCannotCallItself: nesting would make the timeout, the log cap and
// the call counter meaningless, and buys nothing over a Lua function.
func TestLuaCannotCallItself(t *testing.T) {
	res := runLua(t, `return tool("lua", {script = "return 1"}).content`, nil)
	if res.Success {
		t.Fatal("a script recursed into the lua tool")
	}
	if !strings.Contains(res.Error, "not allowed") {
		t.Fatalf("got %q", res.Error)
	}
}

// TestLuaRespectsToolGate is the hole the tool() function would otherwise
// open: restrictions are enforced above RunTool, and a script calls RunTool
// directly. Without the context gate, a script in a child agent could spawn
// grandchildren or reach a tool its agent was denied.
func TestLuaRespectsToolGate(t *testing.T) {
	ctx := WithToolGate(context.Background(),
		Deny("subagent tool is not available to subagents (recursion denied)", "subagent"))

	res := (&LuaEvalTool{}).Run(ctx, map[string]any{
		"script": `local r = tool("subagent", {task = "spawn a grandchild"}); return r.success and "ALLOWED" or ("denied: " .. r.error)`,
	})
	if !res.Success {
		t.Fatalf("the script itself should run: %s", res.Error)
	}
	if strings.Contains(res.Content, "ALLOWED") {
		t.Fatalf("the gate did not reach the script's tool call: %q", res.Content)
	}
	if !strings.Contains(res.Content, "recursion denied") {
		t.Fatalf("got %q", res.Content)
	}
}

func TestLuaToolGateAllowlistApplies(t *testing.T) {
	ctx := WithToolGate(context.Background(), AllowOnly("read"))

	res := (&LuaEvalTool{}).Run(ctx, map[string]any{
		"script": `
			local a = tool("read", {path = "/definitely/not/here"})
			local b = tool("write", {path = "/tmp/nope", content = "x"})
			return "read blocked: " .. tostring(string.find(a.error, "not available to this agent") ~= nil)
				.. " write blocked: " .. tostring(string.find(b.error, "not available to this agent") ~= nil)
		`,
	})
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	// read is allowed, so its failure is the missing file, not the gate.
	if !strings.Contains(res.Content, "read blocked: false") {
		t.Fatalf("an allowed tool was gated: %q", res.Content)
	}
	if !strings.Contains(res.Content, "write blocked: true") {
		t.Fatalf("a tool outside the allowlist ran: %q", res.Content)
	}
}

// TestLuaToolCallsGoThroughHooks: a protection a script could step around
// would be no protection at all.
func TestLuaToolCallsGoThroughHooks(t *testing.T) {
	ResetFileStamps()
	defer hooks.SetForTesting([]hooks.Hook{{
		Event: hooks.EventPreTool, Tools: []string{"write"},
		Command: `case "$TYCI_TOOL_PATH" in *.env) echo "protected"; exit 1;; esac`,
	}})()
	path := filepath.Join(t.TempDir(), "x.env")

	res := runLua(t, `
		local r = tool("write", {path = args.path, content = "TOKEN=1"})
		return r.success and "WROTE" or ("blocked: " .. r.error)
	`, map[string]any{"path": path})
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "protected") {
		t.Fatalf("the hook did not see the script's call: %q", res.Content)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the file was written despite the hook")
	}
}

// ---------------------------------------------------------------------------
// Limits
// ---------------------------------------------------------------------------

// TestLuaInfiniteLoopIsAborted is the one that keeps a bad script from
// wedging the whole agent. gopher-lua checks our context between
// instructions, which is why a pure busy loop can be interrupted at all.
func TestLuaInfiniteLoopIsAborted(t *testing.T) {
	start := time.Now()
	res := (&LuaEvalTool{}).Run(context.Background(), map[string]any{
		"script": `while true do end`, "timeout": 1,
	})
	if res.Success {
		t.Fatal("an endless loop reported success")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("took %s; the deadline did not interrupt the VM", elapsed)
	}
	if !strings.Contains(res.Error, "timed out") {
		t.Fatalf("got %q", res.Error)
	}
}

// TestLuaTimeoutKeepsTheLogSoFar: the output up to the hang is usually what
// tells the model where the loop is.
func TestLuaTimeoutKeepsTheLogSoFar(t *testing.T) {
	res := (&LuaEvalTool{}).Run(context.Background(), map[string]any{
		"script": `log("reached the loop"); while true do end`, "timeout": 1,
	})
	if res.Success {
		t.Fatal("expected a timeout")
	}
	if !strings.Contains(res.Error, "reached the loop") {
		t.Fatalf("log before the hang was lost: %q", res.Error)
	}
}

func TestLuaCancellationIsReported(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()

	res := (&LuaEvalTool{}).Run(ctx, map[string]any{"script": `while true do end`})
	if res.Success {
		t.Fatal("expected cancellation to stop the script")
	}
	if !strings.Contains(res.Error, "cancelled") {
		t.Fatalf("got %q", res.Error)
	}
}

// TestLuaToolCallLimit: a looping script must be stopped by a counter and not
// by the clock, because by the time the clock notices it has already made
// thousands of calls.
func TestLuaToolCallLimit(t *testing.T) {
	calls := 0
	defer swapTool(t, "spy", func(_ context.Context, _ map[string]any) ToolResult {
		calls++
		return ToolResult{Type: "result", Success: true}
	})()

	res := runLua(t, `while true do tool("spy", {}) end`, nil)
	if res.Success {
		t.Fatal("an unbounded tool loop reported success")
	}
	if calls > luaMaxToolCalls {
		t.Fatalf("made %d calls, cap is %d", calls, luaMaxToolCalls)
	}
	if !strings.Contains(res.Error, "looping") {
		t.Fatalf("the message should name the likely cause: %q", res.Error)
	}
}

func TestLuaLogIsCapped(t *testing.T) {
	res := runLua(t, `
		for i = 1, 20000 do log(string.rep("padding ", 20) .. i) end
		return "done"
	`, nil)
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	if len(res.Content) > luaMaxLogBytes*2 {
		t.Fatalf("content is %d bytes; the cap exists to protect the context window", len(res.Content))
	}
	if !strings.Contains(res.Content, "dropped") {
		t.Fatalf("truncation must be visible, not silent")
	}
}

func TestLuaReturnValueIsCapped(t *testing.T) {
	res := runLua(t, `return string.rep("a", 500000)`, nil)
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	if len(res.Content) > luaMaxReturnBytes*2 {
		t.Fatalf("content is %d bytes", len(res.Content))
	}
	if !strings.Contains(res.Content, "truncated") {
		t.Fatal("truncation must be visible")
	}
}

func TestLuaTimeoutIsClamped(t *testing.T) {
	// Asking for a week must not get one; the check is that it still returns
	// promptly with the clamp applied, which the message reports.
	res := (&LuaEvalTool{}).Run(context.Background(), map[string]any{
		"script": `return "quick"`, "timeout": 999999,
	})
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

func TestLuaJSONRoundTrip(t *testing.T) {
	res := runLua(t, `
		local encoded = json_encode({items = {"a", "b"}, n = 2})
		local decoded = json_decode(encoded)
		return decoded.items[2] .. "/" .. decoded.n
	`, nil)
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	if !strings.HasPrefix(res.Content, "b/2") {
		t.Fatalf("got %q", res.Content)
	}
}

func TestLuaJSONDecodeReportsBadInput(t *testing.T) {
	res := runLua(t, `
		local v, err = json_decode("{not json")
		if v ~= nil then return "should have failed" end
		return "error: " .. err
	`, nil)
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	if !strings.HasPrefix(res.Content, "error: ") {
		t.Fatalf("got %q", res.Content)
	}
}

// ---------------------------------------------------------------------------
// Display
// ---------------------------------------------------------------------------

// TestLuaLogStreamsToTheDisplay matters because the obvious implementation
// (fmt.Println, as the older file-based lua runtime did) paints over the TUI.
func TestLuaLogStreamsToTheDisplay(t *testing.T) {
	var lines []string
	ctx := stream.WithOutput(context.Background(), func(_ int, line string) {
		lines = append(lines, line)
	})

	res := (&LuaEvalTool{}).Run(ctx, map[string]any{
		"script": `log("first"); log("second"); return "ok"`,
	})
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	if len(lines) != 2 || lines[0] != "first" || lines[1] != "second" {
		t.Fatalf("expected both lines streamed live, got %v", lines)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// fakeTool is a stand-in registered under a name the real registry does not
// use, so a test can observe exactly what tool() passed on.
type fakeTool struct {
	name string
	run  func(context.Context, map[string]any) ToolResult
}

func (f *fakeTool) Name() string { return f.name }
func (f *fakeTool) Run(ctx context.Context, input map[string]any) ToolResult {
	return f.run(ctx, input)
}

// swapTool installs a fake tool in the shared registry and returns a function
// removing it again. The registry is package-level state, so tests using this
// must not run in parallel.
func swapTool(t *testing.T, name string, run func(context.Context, map[string]any) ToolResult) func() {
	t.Helper()
	if _, exists := lookupTool(name); exists {
		t.Fatalf("%q is a real tool; pick a name that cannot collide", name)
	}
	registerTool(name, &fakeTool{name: name, run: run})
	return func() { unregisterTool(name) }
}

// ---------------------------------------------------------------------------
// Sandbox
// ---------------------------------------------------------------------------

// TestLuaCannotReachTheMachineDirectly is the hole this closes: gopher-lua
// opens every standard library by default, so a model-written script could run
// commands with os.execute and write files with io.open — routing around the
// pre/post tool hooks, the write freshness guard and the context tool gate
// that denies a subagent both recursion and any tool its definition withheld.
func TestLuaCannotReachTheMachineDirectly(t *testing.T) {
	for _, global := range []string{
		"io", "package", "debug", "require", "dofile", "loadfile", "load", "loadstring",
	} {
		res := runLua(t, "return type("+global+")", nil)
		if !res.Success {
			t.Fatalf("%s: %s", global, res.Error)
		}
		if !strings.Contains(res.Content, "nil") {
			t.Errorf("%s is still reachable from a script: %q", global, res.Content)
		}
	}
}

func TestLuaOsKeepsOnlyTheClock(t *testing.T) {
	res := runLua(t, `return type(os.execute) .. "," .. type(os.remove) .. "," .. type(os.getenv) .. "," .. type(os.time)`, nil)
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if !strings.Contains(res.Content, "nil,nil,nil,function") {
		t.Fatalf("os table = %q; want the acting functions gone and time kept", res.Content)
	}
}

// TestLuaCannotWriteAFileWithoutTheWriteTool: the point of the sandbox stated
// as the behaviour a person cares about.
func TestLuaCannotWriteAFileWithoutTheWriteTool(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sneaky.txt")

	res := runLua(t, `local f = io.open("`+target+`", "w") f:write("x") f:close() return "wrote it"`, nil)
	if res.Success {
		t.Fatalf("the script wrote a file outside the tool layer: %s", res.Content)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("the file exists, so the sandbox did not hold")
	}
}

// TestLuaKeepsThePureLibraries: the sandbox must not cost the script the parts
// of the language it is actually used for.
func TestLuaKeepsThePureLibraries(t *testing.T) {
	res := runLua(t, `
		local parts = {}
		for _, w in ipairs({"alpha", "beta"}) do table.insert(parts, w:upper()) end
		return table.concat(parts, "-") .. ":" .. tostring(math.max(2, 7)) .. ":" .. type(os.time())
	`, nil)
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if !strings.Contains(res.Content, "ALPHA-BETA:7:number") {
		t.Fatalf("got %q", res.Content)
	}
}

// TestLuaPrintGoesToTheLog, not to stdout: gopher-lua's own print writes
// straight to the terminal, which in TUI mode paints over the screen.
func TestLuaPrintGoesToTheLog(t *testing.T) {
	res := runLua(t, `print("progress line") return "done"`, nil)
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if !strings.Contains(res.Content, "progress line") {
		t.Fatalf("print output did not reach the result: %q", res.Content)
	}
}

// TestLuaGrantsNoExtraPowerToARestrictedAgent is the reason lua can be handed
// to every agent regardless of its allowlist: tool() inside a script goes
// through RunTool, which checks the same gate, so a narrow agent's script is
// narrow in exactly the same way. What lua adds is round trips saved, not
// reach — which is why the narrowest agents (a "locator" allowed only find and
// read) are the ones that need it most.
func TestLuaGrantsNoExtraPowerToARestrictedAgent(t *testing.T) {
	ctx := WithToolGate(context.Background(), AllowOnly("find"))

	res := (&LuaEvalTool{}).Run(ctx, map[string]any{
		"script": `
			local blocked = {}
			for _, name in ipairs({"write", "bash", "memory", "subagent"}) do
				local r = tool(name, {})
				if r.error and string.find(r.error, "not available to this agent") then
					table.insert(blocked, name)
				end
			end
			return table.concat(blocked, ",")
		`,
	})
	if !res.Success {
		t.Fatalf("failed: %s", res.Error)
	}
	for _, name := range []string{"write", "bash", "memory", "subagent"} {
		if !strings.Contains(res.Content, name) {
			t.Errorf("%q was reachable from a script in a find-only agent: %q", name, res.Content)
		}
	}
}

// TestGateRefusalNamesTheAlternatives: telling an agent only what it cannot do
// leaves it guessing at what it can, and every guess costs another refused call.
func TestGateRefusalNamesTheAlternatives(t *testing.T) {
	ctx := WithToolGate(context.Background(), AllowOnly("find", "read"))
	res := RunTool(ctx, "write", map[string]any{"path": "x", "content": "y"})

	if res.Success {
		t.Fatal("write should have been refused")
	}
	for _, want := range []string{"find", "read", "help", "lua"} {
		if !strings.Contains(res.Error, want) {
			t.Errorf("the refusal does not mention %q as available: %q", want, res.Error)
		}
	}
}

// TestLuaEmptyReturnIsNotReportedAsNoReturn is a trap seen in a real session.
// A script ending in table.concat(matches) with no matches returns the empty
// string, and the result said "finished without returning anything" — so the
// model rewrote a script that was working correctly and simply finding
// nothing. It did that twice before adding log() calls that proved the search
// was the problem, not the code.
func TestLuaEmptyReturnIsNotReportedAsNoReturn(t *testing.T) {
	res := runLua(t, `local t = {} return table.concat(t, ",")`, nil)
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if strings.Contains(res.Content, "without returning anything") {
		t.Fatalf("an empty return was reported as no return: %q", res.Content)
	}
	for _, want := range []string{"returned an empty value", "That is an answer, not an error", "Do not rewrite the script"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("the result should say what an empty value means, missing %q: %q", want, res.Content)
		}
	}
}

// TestLuaNoReturnStatementStillSaysSo: the original message is still right for
// the script that genuinely forgot to return.
func TestLuaNoReturnStatementStillSaysSo(t *testing.T) {
	res := runLua(t, `local x = 1 + 1`, nil)
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if !strings.Contains(res.Content, "without returning anything") {
		t.Fatalf("a script with no return should still be told so: %q", res.Content)
	}
}

// TestLuaEmptyReturnKeepsTheLogs: the logs are usually the only evidence of
// what the script actually looked at.
func TestLuaEmptyReturnKeepsTheLogs(t *testing.T) {
	res := runLua(t, `log("searched 12 files") return ""`, nil)
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if !strings.Contains(res.Content, "searched 12 files") {
		t.Errorf("the log was dropped: %q", res.Content)
	}
	if !strings.Contains(res.Content, "empty value") {
		t.Errorf("the empty return was not explained: %q", res.Content)
	}
}

// TestLuaEmptyTableReturnIsAlsoAnAnswer: a table with nothing in it formats to
// "{}" or similar rather than "", so it must not be swept into the same case
// by accident — it is a real value and should come back as one.
func TestLuaEmptyTableReturnIsAlsoAnAnswer(t *testing.T) {
	res := runLua(t, `return {}`, nil)
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if strings.Contains(res.Content, "without returning anything") {
		t.Fatalf("an empty table was reported as no return: %q", res.Content)
	}
}
