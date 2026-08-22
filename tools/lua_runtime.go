package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	lua "github.com/yuin/gopher-lua"

	"github.com/decodo/tyci/stream"
)

const (
	// luaGlobLimit bounds ctx.glob's result. Matches the old find | head -500.
	luaGlobLimit = 500

	// luaReadLimit bounds ctx.read. Generous enough for source files, small
	// enough that a stray read of a binary or a log does not become a
	// multi-megabyte Lua string on its way into a tool result.
	luaReadLimit = 1 << 20
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

// ctxRun executes a shell command and returns stdout.
func (lc *LuaContext) ctxRun(L *lua.LState) int {
	cmdStr := L.CheckString(1)

	cmd := exec.CommandContext(lc.ctx, "sh", "-c", cmdStr)
	// Capped: an unbounded CombinedOutput here meant one `cat` of a large
	// file could pull hundreds of megabytes into the Lua string and from
	// there into a tool result.
	var buf cappedBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()

	output := buf.result()
	if err != nil {
		L.Push(lua.LString(output + "\nError: " + err.Error()))
	} else {
		L.Push(lua.LString(strings.TrimSpace(output)))
	}
	return 1
}

// ctxRead reads a file and returns its contents.
func (lc *LuaContext) ctxRead(L *lua.LState) int {
	path := L.CheckString(1)

	data, err := os.ReadFile(path)
	if err != nil {
		L.Push(lua.LString(""))
		L.Push(lua.LString(err.Error()))
		return 2
	}
	if len(data) > luaReadLimit {
		L.Push(lua.LString(string(data[:luaReadLimit])))
		L.Push(lua.LString(fmt.Sprintf("truncated: %s is %d bytes, only the first %d were read", path, len(data), luaReadLimit)))
		return 2
	}

	L.Push(lua.LString(string(data)))
	return 1
}

// ctxWrite writes content to a file.
func (lc *LuaContext) ctxWrite(L *lua.LState) int {
	path := L.CheckString(1)
	content := L.CheckString(2)

	// Create parent directory if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		L.Push(lua.LBool(false))
		L.Push(lua.LString(err.Error()))
		return 2
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		L.Push(lua.LBool(false))
		L.Push(lua.LString(err.Error()))
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
	tmpFile.Close()
	L.Push(lua.LString(tmpFile.Name()))
	return 1
}

// GetOutput returns the accumulated output from ctx.log calls.
func (lc *LuaContext) GetOutput() string {
	return lc.output.String()
}
