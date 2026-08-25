package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/decodo/tyci/stream"
)

// The "lua" tool: a script the model writes inline, with every other tyci
// tool callable from inside it.
//
// This is not a second shell. The reason it exists is that the model pays a
// full request/response round trip for every tool call, so anything shaped
// like "for each of these N things, do X, and decide what to do next from the
// result" costs N round trips — and N is usually not known until the first
// one comes back. A script collapses that to one call: the loop, the
// branching and the accumulation happen in the script, and only the
// conclusion is sent back through the context window.
//
// The two things that make it worth having over bash are tool() — which
// reaches read/write/find/subagent/bash and every MCP tool, with hooks and
// the write-freshness guard still applying, because it goes through RunTool
// like any other call — and real data structures, so a script can build a
// list of subagent tasks and fan out in one go.

const (
	// LuaDefaultTimeoutSec bounds a script by wall clock. Generous compared
	// with a shell command because a script's whole point is to do many
	// things in one call, and a fan-out over subagents legitimately takes
	// minutes.
	LuaDefaultTimeoutSec = 300

	// LuaMaxTimeoutSec caps what a script may ask for. Nothing collects a
	// lua script's result in the background the way bash's job handoff does,
	// so a script that outlives the model's patience is simply lost work.
	LuaMaxTimeoutSec = 1800

	// luaMaxLogBytes caps what log() may contribute to one result. A script
	// looping over a thousand files can print a great deal, and the model
	// asked for a conclusion, not a transcript.
	luaMaxLogBytes = 32 * 1024

	// luaMaxReturnBytes caps the returned value. Separate from the log cap
	// because the return value is the answer and deserves its own budget.
	luaMaxReturnBytes = 64 * 1024

	// luaMaxToolCalls bounds how many tools one script may call. Without it,
	// a loop bug turns into thousands of subagent spawns or file writes
	// before the timeout notices, and the timeout is the wrong backstop for
	// that: the damage is done by then.
	luaMaxToolCalls = 500
)

// luaDepthCtxKey marks that we are already inside a lua script, so tool()
// cannot call "lua" again. Nesting would buy nothing a plain Lua function
// call does not already give, while making timeouts, caps and call counting
// meaningless.
type luaDepthCtxKey struct{}

type LuaEvalTool struct{}

func (t *LuaEvalTool) Name() string { return "lua" }

func (t *LuaEvalTool) Run(ctx context.Context, input map[string]any) ToolResult {
	script, ok := input["script"].(string)
	if !ok || strings.TrimSpace(script) == "" {
		return validationResult("script is required (a Lua chunk; use return to hand a value back)")
	}

	timeoutSec := intParam(input, "timeout", LuaDefaultTimeoutSec)
	if timeoutSec <= 0 {
		timeoutSec = LuaDefaultTimeoutSec
	}
	if timeoutSec > LuaMaxTimeoutSec {
		timeoutSec = LuaMaxTimeoutSec
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	runCtx = context.WithValue(runCtx, luaDepthCtxKey{}, true)

	env := &luaEnv{ctx: runCtx}

	L := lua.NewState(lua.Options{
		// Scripts here are written by the model on the spot and thrown away,
		// so a small stack that grows on demand fits them better than
		// reserving the default up front on every call.
		MinimizeStackMemory: true,
	})
	defer L.Close()

	// Makes the VM abort on our deadline: gopher-lua checks this context
	// between instructions, which is what stops a runaway `while true do end`
	// instead of hanging the agent until the session dies.
	L.SetContext(runCtx)

	env.install(L)
	restrictLuaStdlib(L, env)

	if args, ok := input["args"].(map[string]any); ok {
		L.SetGlobal("args", convertToLuaTable(L, args))
	} else {
		L.SetGlobal("args", L.NewTable())
	}

	err := L.DoString(script)
	logs := env.logs()

	if err != nil {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   luaFailure(err, runCtx, timeoutSec, env.toolCalls(), logs),
		}
	}

	// The chunk's return value, if it returned one. A script that only
	// log()s and returns nothing is fine — its output is the log.
	// returned distinguishes "the script had no return statement" from "the
	// script returned a value that happens to be empty". Conflating them was
	// a real trap: a script ending in table.concat(matches) with no matches
	// returns the empty string, and being told it "finished without returning
	// anything" sent one model rewriting a script that was working correctly
	// and simply finding nothing.
	var ret string
	returned := L.GetTop() > 0
	if returned {
		ret = formatLuaValue(L.Get(-1))
		L.Pop(1)
		if len(ret) > luaMaxReturnBytes {
			ret = ret[:luaMaxReturnBytes] + fmt.Sprintf("\n[return value truncated at %d bytes]", luaMaxReturnBytes)
		}
	}

	return ToolResult{Type: "result", Success: true, Content: luaSuccess(logs, ret, returned, env.toolCalls())}
}

// luaFailure builds the error text for a script that did not finish. It has
// to distinguish the three causes, because the fix differs: a timeout means
// the script was too ambitious or looping, the call cap means it was looping,
// and a Lua error means the code is wrong. In all three cases the log so far
// is the most useful thing we have, so it is kept.
func luaFailure(err error, ctx context.Context, timeoutSec, calls int, logs string) string {
	var b strings.Builder

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		fmt.Fprintf(&b, "lua script timed out after %ds (aborted mid-execution; anything it had already written or spawned still happened). Split the work into smaller calls, or raise timeout up to %d.", timeoutSec, LuaMaxTimeoutSec)
	case errors.Is(ctx.Err(), context.Canceled):
		b.WriteString("lua script was cancelled")
	default:
		fmt.Fprintf(&b, "lua error: %v", err)
	}

	fmt.Fprintf(&b, "\ntool calls made before stopping: %d", calls)
	if logs != "" {
		b.WriteString("\n\noutput so far:\n")
		b.WriteString(logs)
	}
	return b.String()
}

func luaSuccess(logs, ret string, returned bool, calls int) string {
	// An empty value that WAS returned is a result, not a mistake: the script
	// ran, and what it found was nothing. Saying so plainly is the difference
	// between the model moving on and the model rewriting working code.
	if returned && ret == "" {
		empty := "script ran fine and returned an empty value" +
			fmt.Sprintf(" (%d tool calls). That is an answer, not an error: whatever it searched for, it found nothing. Do not rewrite the script to fix this — check the assumption it was testing.", calls)
		if logs != "" {
			return logs + "\n--- returned ---\n" + empty
		}
		return empty
	}

	switch {
	case logs == "" && ret == "":
		return fmt.Sprintf("script finished without returning anything or logging (%d tool calls). Use return to hand a value back, or log() to report progress.", calls)
	case ret == "":
		return logs
	case logs == "":
		return ret
	}
	return logs + "\n--- returned ---\n" + ret
}

// ---------------------------------------------------------------------------
// The script environment
// ---------------------------------------------------------------------------

// luaEnv holds the per-run state the exposed functions share. Guarded by a
// mutex because a script may call a tool that streams from another goroutine,
// and because the log is read after the VM aborts on a deadline.
type luaEnv struct {
	ctx context.Context

	mu       sync.Mutex
	log      strings.Builder
	dropped  int
	calls    int
	streamed bool
}

// restrictLuaStdlib removes the parts of the Lua standard library that would
// let a script act on the machine without going through a tool.
//
// This closes a real hole rather than tidying up. gopher-lua opens every
// standard library by default, so before this a model-written script could
// call os.execute to run a command and io.open to write a file — and each of
// those routes around everything the tool layer is for: pre_tool hooks that
// veto a path, post_tool hooks that lint what was written, the write
// freshness guard that stops one agent silently overwriting another's work,
// and the context tool gate that denies a subagent both recursion and any
// tool its definition did not allow. require and loadfile are the same hole
// one step removed: they load code from disk.
//
// What is left is the pure part of the language — string, table, math,
// coroutine, plus os.time/os.date/os.clock, which compute rather than act.
// print is rebound onto log() because gopher-lua's own print writes straight
// to stdout, which in TUI mode paints over the screen.
func restrictLuaStdlib(L *lua.LState, env *luaEnv) {
	for _, name := range []string{
		"io",       // io.open, io.lines: unmediated file access
		"package",  // loads Lua modules from disk
		"debug",    // reaches into the VM, including other closures' upvalues
		"require",  // same as package
		"dofile",   // runs a file
		"loadfile", // reads a file
		"load",     // dynamic code; nothing a written-on-the-spot script needs
		"loadstring",
		"collectgarbage",
	} {
		L.SetGlobal(name, lua.LNil)
	}

	// os keeps only the clock functions. Replacing the table wholesale rather
	// than deleting keys from it, so a future gopher-lua release that adds
	// another os function does not quietly widen this.
	if osTable, ok := L.GetGlobal("os").(*lua.LTable); ok {
		safe := L.NewTable()
		for _, fn := range []string{"time", "date", "clock", "difftime"} {
			if v := osTable.RawGetString(fn); v != lua.LNil {
				safe.RawSetString(fn, v)
			}
		}
		L.SetGlobal("os", safe)
	}

	// print goes to the collected log, not to the terminal.
	L.SetGlobal("print", L.NewFunction(env.luaLog))
}

func (e *luaEnv) install(L *lua.LState) {
	L.SetGlobal("tool", L.NewFunction(e.luaTool))
	L.SetGlobal("log", L.NewFunction(e.luaLog))
	L.SetGlobal("json_encode", L.NewFunction(e.luaJSONEncode))
	L.SetGlobal("json_decode", L.NewFunction(e.luaJSONDecode))
}

// luaTool is tool(name, args) -> {success, content, error}.
//
// It goes through RunTool, not through the tool's Run method, so a script's
// calls are indistinguishable from the model's: user hooks fire, the write
// freshness guard applies, MCP tools resolve. That is deliberate — a
// protection that a script could step around would be no protection.
func (e *luaEnv) luaTool(L *lua.LState) int {
	name := L.CheckString(1)

	if name == "lua" {
		L.RaiseError("tool(\"lua\") is not allowed from inside a lua script — call the work directly, or use a Lua function")
		return 0
	}

	var args map[string]any
	if L.GetTop() >= 2 {
		switch v := L.Get(2).(type) {
		case *lua.LTable:
			args = convertLuaTableToMap(v)
		case *lua.LNilType:
		default:
			L.RaiseError("tool(%q, ...): second argument must be a table of arguments, got %s", name, v.Type().String())
			return 0
		}
	}

	e.mu.Lock()
	e.calls++
	n := e.calls
	e.mu.Unlock()
	if n > luaMaxToolCalls {
		L.RaiseError("this script has made %d tool calls, which is the limit — it is almost certainly looping. Narrow the work or batch it", luaMaxToolCalls)
		return 0
	}

	res := RunTool(e.ctx, name, args)

	tbl := L.NewTable()
	L.SetField(tbl, "success", lua.LBool(res.Success))
	L.SetField(tbl, "content", lua.LString(res.Content))
	L.SetField(tbl, "error", lua.LString(res.Error))
	L.Push(tbl)
	return 1
}

// luaLog is log(...): progress for the human watching, and part of the result
// for the model. Accepts several arguments like print does, because a script
// author reaches for that shape without thinking.
func (e *luaEnv) luaLog(L *lua.LState) int {
	parts := make([]string, 0, L.GetTop())
	for i := 1; i <= L.GetTop(); i++ {
		parts = append(parts, formatLuaValue(L.Get(i)))
	}
	e.appendLog(strings.Join(parts, " "))
	return 0
}

func (e *luaEnv) appendLog(line string) {
	// Forward to the display first, so a long-running script shows progress
	// live in the tool block instead of going quiet for minutes. Note this is
	// stream.Output and not fmt.Println: printing to stdout from a tool
	// paints over the TUI.
	if out := stream.Output(e.ctx); out != nil {
		idx, _ := e.ctx.Value(stream.ToolIdxCtxKey{}).(int)
		out(idx, line)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.log.Len()+len(line)+1 > luaMaxLogBytes {
		e.dropped++
		return
	}
	if e.log.Len() > 0 {
		e.log.WriteString("\n")
	}
	e.log.WriteString(line)
}

func (e *luaEnv) logs() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.log.String()
	if e.dropped > 0 {
		out += fmt.Sprintf("\n[%d further log line(s) dropped: the %d KiB log cap was reached]", e.dropped, luaMaxLogBytes/1024)
	}
	return out
}

func (e *luaEnv) toolCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// luaJSONEncode and luaJSONDecode exist because tool results are text: a
// script that reads structured output (an MCP tool, a `--json` command) would
// otherwise have to parse it with string matching.
func (e *luaEnv) luaJSONEncode(L *lua.LState) int {
	data, err := json.Marshal(convertLuaValueToGo(L.Get(1)))
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(lua.LString(data))
	return 1
}

func (e *luaEnv) luaJSONDecode(L *lua.LState) int {
	var v any
	if err := json.Unmarshal([]byte(L.CheckString(1)), &v); err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(convertToLuaValue(L, v))
	return 1
}

// formatLuaValue renders a Lua value for a human and a model to read. Tables
// become JSON, because a script that builds up a result table wants it
// readable rather than "table: 0x14000...".
func formatLuaValue(v lua.LValue) string {
	if tbl, ok := v.(*lua.LTable); ok {
		if data, err := json.MarshalIndent(convertLuaTable(tbl), "", "  "); err == nil {
			return string(data)
		}
	}
	return v.String()
}
