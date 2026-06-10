package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

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
	output, err := cmd.CombinedOutput()

	if err != nil {
		L.Push(lua.LString(string(output) + "\nError: " + err.Error()))
	} else {
		L.Push(lua.LString(strings.TrimSpace(string(output))))
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
func (lc *LuaContext) ctxGlob(L *lua.LState) int {
	pattern := L.CheckString(1)

	// Use bash glob for simplicity
	cmd := exec.CommandContext(lc.ctx, "bash", "-c", fmt.Sprintf("find . -name '%s' -type f | head -500", pattern))
	output, err := cmd.Output()

	if err != nil {
		L.Push(L.NewTable())
		return 1
	}

	files := strings.Split(strings.TrimSpace(string(output)), "\n")
	arr := L.NewTable()
	for i, f := range files {
		if f != "" {
			arr.RawSetInt(i+1, lua.LString(f))
		}
	}

	L.Push(arr)
	return 1
}

// ctxLog logs a message.
func (lc *LuaContext) ctxLog(L *lua.LState) int {
	msg := L.CheckString(1)
	lc.output.WriteString(msg)
	lc.output.WriteString("\n")
	fmt.Println(msg)
	return 0
}

// ctxWarn logs a warning message.
func (lc *LuaContext) ctxWarn(L *lua.LState) int {
	msg := L.CheckString(1)
	fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
	return 0
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
