package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	lua "github.com/yuin/gopher-lua"

	"github.com/crazy-goat/tyci-agent/stream"
)

const (
	// luaGlobLimit bounds ctx.glob's result. Matches the old find | head -500.
	luaGlobLimit = 500
)

// errGlobLimit stops a glob walk once the limit is reached. doublestar has no
// "stop everything" sentinel of its own — SkipDir only skips the current
// directory — so this is returned from the callback and swallowed by the
// caller.
var errGlobLimit = errors.New("glob limit reached")

// LuaContext provides a sandboxed API for Lua scripts.
type LuaContext struct {
	ctx    context.Context
	L      *lua.LState
	output strings.Builder
}

// newLuaContext creates a new sandboxed Lua context.
func newLuaContext(ctx context.Context, L *lua.LState) *lua.LTable {
	lc := &LuaContext{
		ctx: ctx,
		L:   L,
	}

	tbl := L.NewTable()

	// Register ctx functions
	L.SetField(tbl, "run", L.NewFunction(lc.ctxRun))
	L.SetField(tbl, "read", L.NewFunction(lc.ctxRead))
	L.SetField(tbl, "write", L.NewFunction(lc.ctxWrite))
	L.SetField(tbl, "glob", L.NewFunction(lc.ctxGlob))
	L.SetField(tbl, "log", L.NewFunction(lc.ctxLog))
	L.SetField(tbl, "warn", L.NewFunction(lc.ctxWarn))
	L.SetField(tbl, "tempfile", L.NewFunction(lc.ctxTempfile))

	return tbl
}

// ctxRun runs a shell command through the bash tool and returns stdout.
//
// It goes through RunTool, so the pre_tool and post_tool hooks, the tool gate
// and the worktree mutation queue apply, as they do to a model's bash call.
// On failure the tool's error text is returned instead of stdout.
func (lc *LuaContext) ctxRun(L *lua.LState) int {
	cmdStr := L.CheckString(1)
	res := RunTool(lc.ctx, "bash", map[string]any{"command": cmdStr})
	if !res.Success {
		L.Push(lua.LString(res.Error))
		return 1
	}
	L.Push(lua.LString(strings.TrimSpace(res.Content)))
	return 1
}

// ctxRead reads a file through the read tool and returns its contents.
//
// On failure it returns an empty string and the error text. When the read
// tool cuts the file, the second value holds its notice, as the direct read
// did before, so a script can tell a partial read from a whole one.
func (lc *LuaContext) ctxRead(L *lua.LState) int {
	path := L.CheckString(1)
	res := RunTool(lc.ctx, "read", map[string]any{"path": path})
	if !res.Success {
		L.Push(lua.LString(""))
		L.Push(lua.LString(res.Error))
		return 2
	}

	text, notice := splitReadNotice(res.Content)
	L.Push(lua.LString(text))
	if notice != "" {
		L.Push(lua.LString(notice))
		return 2
	}
	return 1
}

// The texts that the read tool (read.go) puts in its result when it cuts a
// file. The read tool has no flag for this, and its result is shown to the
// model, so the marks are matched here. TestLuaTool_CtxReadReturnsNotice
// fails if the wording in read.go changes.
const (
	readContinuationMark = "\n\n[Showing lines "
	readFirstLineMark    = "No partial line returned."
)

// splitReadNotice separates the read tool's notice from the file text. When
// the first line alone is over the byte cap, the read tool returns only its
// notice, so the text is empty.
func splitReadNotice(content string) (text, notice string) {
	if strings.Contains(content, readFirstLineMark) {
		return "", content
	}
	if i := strings.LastIndex(content, readContinuationMark); i >= 0 {
		return content[:i], content[i+2:]
	}
	return content, ""
}

// ctxWrite writes content to a file through the write tool.
//
// It returns true on success. On failure it returns false and the error text.
func (lc *LuaContext) ctxWrite(L *lua.LState) int {
	path := L.CheckString(1)
	content := L.CheckString(2)
	res := RunTool(lc.ctx, "write", map[string]any{"path": path, "content": content})
	if !res.Success {
		L.Push(lua.LBool(false))
		L.Push(lua.LString(res.Error))
		return 2
	}

	L.Push(lua.LBool(true))
	return 1
}

// ctxGlob finds files matching a pattern.
//
// Matched in-process with doublestar rather than by shelling out to find.
// The shell version interpolated the caller's pattern straight into a
// `bash -c` string, so a pattern containing a quote and a semicolon ran as a
// command — and the pattern can come from a tool argument, i.e. from the
// model. Doing the matching here also means ** works, which the find -name
// version never did.
func (lc *LuaContext) ctxGlob(L *lua.LState) int {
	pattern := L.CheckString(1)

	arr := L.NewTable()
	if !doublestar.ValidatePattern(pattern) {
		L.Push(arr)
		L.Push(lua.LString(fmt.Sprintf("invalid glob pattern %q", pattern)))
		return 2
	}

	// A bare name like "*.go" is what a caller means to find anywhere in the
	// tree, which is what the old find -name did; keep that.
	if !strings.ContainsAny(pattern, "/") {
		pattern = "**/" + pattern
	}

	count := 0
	err := doublestar.GlobWalk(os.DirFS("."), pattern, func(path string, d os.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		count++
		arr.RawSetInt(count, lua.LString(path))
		if count >= luaGlobLimit {
			return errGlobLimit
		}
		return nil
	})
	if errors.Is(err, errGlobLimit) {
		err = nil
	}
	if err != nil {
		L.Push(arr)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	L.Push(arr)
	return 1
}

// ctxLog logs a message.
//
// Routed through the display's streaming hook, not fmt.Println: a tool
// writing to stdout paints over the TUI's own frame, and in --print mode it
// corrupts the output the caller is parsing. Where no display is wired (a
// plain test), it is simply collected.
func (lc *LuaContext) ctxLog(L *lua.LState) int {
	msg := L.CheckString(1)
	lc.output.WriteString(msg)
	lc.output.WriteString("\n")
	lc.emit(msg)
	return 0
}

// ctxWarn logs a warning message. Same reasoning as ctxLog — stderr is no
// safer than stdout for a terminal UI — with the level kept in the text so
// the distinction survives.
func (lc *LuaContext) ctxWarn(L *lua.LState) int {
	msg := L.CheckString(1)
	line := "Warning: " + msg
	lc.output.WriteString(line)
	lc.output.WriteString("\n")
	lc.emit(line)
	return 0
}

func (lc *LuaContext) emit(line string) {
	if out := stream.Output(lc.ctx); out != nil {
		idx, _ := lc.ctx.Value(stream.ToolIdxCtxKey{}).(int)
		out(idx, line)
	}
}

// ctxTempfile returns a temporary file path.
func (lc *LuaContext) ctxTempfile(L *lua.LState) int {
	tmpFile, err := os.CreateTemp("", "lua-tool-*.tmp")
	if err != nil {
		L.Push(lua.LString(""))
		return 1
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpFile.Name())
		L.Push(lua.LString(""))
		return 1
	}
	L.Push(lua.LString(tmpFile.Name()))
	return 1
}

// GetOutput returns the accumulated output from ctx.log calls.
func (lc *LuaContext) GetOutput() string {
	return lc.output.String()
}
