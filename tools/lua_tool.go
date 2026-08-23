package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
	"github.com/yuin/gopher-lua/parse"
)

// LuaRun records one execution of a Lua tool script: when it started, how
// long it took, and whether it succeeded. Lua tools (unlike bash) always run
// synchronously to completion inside one Run call — there is no
// backgrounded, still-running state to show — so this is a thin, real
// history rather than a live registry, for the sidebar's Lua tab (TODO
// item 1).
type LuaRun struct {
	Name      string
	StartedAt time.Time
	Duration  time.Duration
	Success   bool
	Error     string
}

// maxRetainedLuaRuns bounds the in-memory run history, mirroring
// jobs.maxRetainedTerminalJobs: process-local, unbounded growth is not
// acceptable for a long session.
const maxRetainedLuaRuns = 50

var (
	luaRunsMu sync.Mutex
	luaRuns   []LuaRun
)

// recordLuaRun appends r to the bounded history, dropping the oldest run
// once the cap is exceeded.
func recordLuaRun(r LuaRun) {
	luaRunsMu.Lock()
	defer luaRunsMu.Unlock()
	luaRuns = append(luaRuns, r)
	if len(luaRuns) > maxRetainedLuaRuns {
		luaRuns = luaRuns[len(luaRuns)-maxRetainedLuaRuns:]
	}
}

// LuaRunHistory returns the most recent Lua tool runs (oldest first, capped
// at maxRetainedLuaRuns) — the Lua sidebar tab's data source. A copy, so the
// caller can't mutate the shared history.
func LuaRunHistory() []LuaRun {
	luaRunsMu.Lock()
	defer luaRunsMu.Unlock()
	out := make([]LuaRun, len(luaRuns))
	copy(out, luaRuns)
	return out
}

// LuaTool implements the Tool interface for user-defined Lua scripts.
type LuaTool struct {
	name        string
	description string
	parameters  map[string]any
	scriptPath  string

	// protoOnce/proto/protoErr cache the compiled bytecode for scriptPath so
	// Run doesn't re-read and re-parse the file from disk on every call —
	// only the first call (or loadLuaTool, which primes it) pays that cost.
	//
	// A *lua.FunctionProto is immutable (bytecode + constants) once compiled,
	// so it's safe to share across goroutines. What is NOT safe to share is
	// a *lua.LState or an *lua.LFunction bound to one — gopher-lua states are
	// not concurrency-safe, and an LFunction's Env is a specific state's
	// global table. So each Run call still builds its own fresh LState and
	// its own LFunction (via newTopLevelFunction) from the shared proto,
	// rather than reusing one LFunction/LState across calls.
	protoOnce sync.Once
	proto     *lua.FunctionProto
	protoErr  error
}

// Name returns the tool name.
func (t *LuaTool) Name() string {
	return t.name
}

// loadProto compiles t.scriptPath into bytecode on first use and caches the
// result for every later call, so the script's source is read and parsed
// from disk exactly once per process regardless of how many times the tool
// is run.
func (t *LuaTool) loadProto() (*lua.FunctionProto, error) {
	t.protoOnce.Do(func() {
		f, err := os.Open(t.scriptPath)
		if err != nil {
			t.protoErr = fmt.Errorf("failed to open lua script: %w", err)
			return
		}
		defer f.Close()

		chunk, err := parse.Parse(f, t.scriptPath)
		if err != nil {
			t.protoErr = fmt.Errorf("failed to parse lua script: %w", err)
			return
		}
		proto, err := lua.Compile(chunk, t.scriptPath)
		if err != nil {
			t.protoErr = fmt.Errorf("failed to compile lua script: %w", err)
			return
		}
		t.proto = proto
	})
	return t.proto, t.protoErr
}

// newTopLevelFunction builds a fresh top-level chunk LFunction from a cached
// proto, bound to L's own global table — the same shape lua.LState.Load
// produces (see gopher-lua's state.go), just without re-parsing the source.
func newTopLevelFunction(L *lua.LState, proto *lua.FunctionProto) *lua.LFunction {
	return &lua.LFunction{
		IsG:      false,
		Env:      L.Env,
		Proto:    proto,
		Upvalues: make([]*lua.Upvalue, 0),
	}
}

// Run executes the Lua tool with the given input, recording it into the
// process-local run history (see LuaRunHistory) regardless of outcome.
func (t *LuaTool) Run(ctx context.Context, input map[string]any) ToolResult {
	started := time.Now()
	res := t.run(ctx, input)
	recordLuaRun(LuaRun{
		Name:      t.name,
		StartedAt: started,
		Duration:  time.Since(started),
		Success:   res.Success,
		Error:     res.Error,
	})
	return res
}

// run is Run's actual body, unwrapped so Run can time and record it above.
func (t *LuaTool) run(ctx context.Context, input map[string]any) ToolResult {
	proto, err := t.loadProto()
	if err != nil {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   fmt.Sprintf("failed to load lua script: %v", err),
		}
	}

	L := lua.NewState()
	defer L.Close()

	// Set up sandboxed context
	sandbox := newLuaContext(ctx, L)
	L.SetGlobal("ctx", sandbox)

	// Execute the cached bytecode (no disk read/parse on this path).
	L.Push(newTopLevelFunction(L, proto))
	if err := L.PCall(0, lua.MultRet, nil); err != nil {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   fmt.Sprintf("failed to load lua script: %v", err),
		}
	}

	// Get the returned table
	result := L.Get(-1)
	L.Pop(1)

	table, ok := result.(*lua.LTable)
	if !ok {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   "lua script must return a table with schema and run",
		}
	}

	// Get the run function
	runVal := table.RawGetString("run")
	if runVal.Type() != lua.LTFunction {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   "lua script table must have a 'run' function",
		}
	}

	// Convert input to Lua table
	argsTable := convertToLuaTable(L, input)

	// Call the run function
	if err := L.CallByParam(lua.P{
		Fn:      runVal.(lua.LValue),
		NRet:    1,
		Protect: true,
	}, sandbox, argsTable); err != nil {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   fmt.Sprintf("lua execution error: %v", err),
		}
	}

	// Get the result
	luaResult := L.Get(-1)
	L.Pop(1)

	return convertLuaResult(luaResult)
}

// convertToLuaTable converts a Go map to a Lua table.
func convertToLuaTable(L *lua.LState, m map[string]any) *lua.LTable {
	t := L.NewTable()
	for k, v := range m {
		lVal := convertToLuaValue(L, v)
		t.RawSetString(k, lVal)
	}
	return t
}

// convertToLuaValue converts a Go value to a Lua value.
func convertToLuaValue(L *lua.LState, v any) lua.LValue {
	switch val := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(val)
	case int:
		return lua.LNumber(val)
	case int64:
		return lua.LNumber(val)
	case float64:
		return lua.LNumber(val)
	case string:
		return lua.LString(val)
	case []any:
		arr := L.NewTable()
		for i, item := range val {
			arr.RawSetInt(i+1, convertToLuaValue(L, item))
		}
		return arr
	case map[string]any:
		return convertToLuaTable(L, val)
	default:
		return lua.LString(fmt.Sprintf("%v", val))
	}
}

// convertLuaResult converts a Lua result table to a ToolResult.
func convertLuaResult(result lua.LValue) ToolResult {
	table, ok := result.(*lua.LTable)
	if !ok {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   "lua run function must return a table",
		}
	}

	tr := ToolResult{
		Type:    "result",
		Success: true,
	}

	// Get success field
	successVal := table.RawGetString("success")
	if successVal.Type() == lua.LTBool {
		tr.Success = bool(successVal.(lua.LBool))
	}

	// Get content field
	contentVal := table.RawGetString("content")
	if contentVal.Type() == lua.LTString {
		tr.Content = contentVal.String()
	}

	// Get error field
	errorVal := table.RawGetString("error")
	if errorVal.Type() == lua.LTString {
		tr.Error = errorVal.String()
	}

	return tr
}

// LoadLuaTools loads all Lua tools from the given directory.
func LoadLuaTools(dir string) ([]*LuaTool, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read lua tools directory: %w", err)
	}

	var tools []*LuaTool
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lua") {
			continue
		}

		scriptPath := filepath.Join(dir, entry.Name())
		tool, err := loadLuaTool(scriptPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load lua tool %s: %v\n", entry.Name(), err)
			continue
		}
		tools = append(tools, tool)
	}

	return tools, nil
}

// loadLuaTool loads a single Lua tool from a script file. It compiles the
// script (priming the LuaTool's proto cache, see loadProto) and runs it once
// to pull out the schema table and validate that a run function is present;
// later calls to LuaTool.Run reuse the cached bytecode instead of
// re-reading and re-parsing the file.
func loadLuaTool(scriptPath string) (*LuaTool, error) {
	t := &LuaTool{scriptPath: scriptPath}
	proto, err := t.loadProto()
	if err != nil {
		return nil, err
	}

	L := lua.NewState()
	defer L.Close()

	L.Push(newTopLevelFunction(L, proto))
	if err := L.PCall(0, lua.MultRet, nil); err != nil {
		return nil, fmt.Errorf("failed to execute lua script: %w", err)
	}

	// Get the returned table
	result := L.Get(-1)
	L.Pop(1)

	table, ok := result.(*lua.LTable)
	if !ok {
		return nil, fmt.Errorf("lua script must return a table")
	}

	// Get schema
	schemaVal := table.RawGetString("schema")
	if schemaVal.Type() != lua.LTTable {
		return nil, fmt.Errorf("lua script table must have a 'schema' table")
	}

	schema := schemaVal.(*lua.LTable)

	// Extract name
	nameVal := schema.RawGetString("name")
	if nameVal.Type() != lua.LTString {
		return nil, fmt.Errorf("schema must have a 'name' string")
	}
	name := nameVal.String()

	// Extract description
	descVal := schema.RawGetString("description")
	description := ""
	if descVal.Type() == lua.LTString {
		description = descVal.String()
	}

	// Extract parameters
	params := make(map[string]any)
	paramsVal := schema.RawGetString("parameters")
	if paramsVal.Type() == lua.LTTable {
		params = convertLuaTableToMap(paramsVal.(*lua.LTable))
	}

	// Get run function — validated here (once, at load time) rather than
	// kept: the closure itself is bound to this throwaway L's env, so it
	// can't be reused by a later Run call. Run re-derives it by re-executing
	// the (now cached, no longer re-parsed) script against its own LState.
	runVal := table.RawGetString("run")
	if runVal.Type() != lua.LTFunction {
		return nil, fmt.Errorf("lua script table must have a 'run' function")
	}

	t.name = name
	t.description = description
	t.parameters = params
	return t, nil
}

// convertLuaTableToMap converts a Lua table to a Go map, dropping any
// non-string keys.
//
// Prefer convertLuaTable for anything crossing into tool arguments: Lua has
// one table type for both maps and arrays, and this function silently loses
// every array element (their keys are numbers, not strings). That mattered
// the moment scripts could call tools — tool("subagent", {tasks = {...}})
// would have arrived with an empty tasks list and no error anywhere.
func convertLuaTableToMap(t *lua.LTable) map[string]any {
	result := make(map[string]any)
	t.ForEach(func(key, value lua.LValue) {
		if keyStr, ok := key.(lua.LString); ok {
			result[string(keyStr)] = convertLuaValueToGo(value)
		}
	})
	return result
}

// convertLuaTable converts a Lua table to either a Go slice or a Go map,
// depending on its shape. A table is treated as an array when it has at least
// one entry and its keys are exactly 1..n — the same rule Lua's own ipairs
// and table.insert work by, so it matches what a script author intends.
//
// An empty table is ambiguous (it is equally a list of nothing and a record
// with no fields) and becomes an empty map, because that is what a JSON
// encoder does with it and the tools on the receiving end take objects.
func convertLuaTable(t *lua.LTable) any {
	n := t.Len() // Lua's array length: the n of a 1..n run
	if n > 0 {
		// Confirm there are no string keys hiding alongside the array part;
		// a mixed table is a record that happens to have numbered fields, and
		// turning it into a list would drop them.
		mixed := false
		t.ForEach(func(key, _ lua.LValue) {
			if _, ok := key.(lua.LString); ok {
				mixed = true
			}
		})
		if !mixed {
			arr := make([]any, 0, n)
			for i := 1; i <= n; i++ {
				arr = append(arr, convertLuaValueToGo(t.RawGetInt(i)))
			}
			return arr
		}
	}
	return convertLuaTableToMap(t)
}

// convertLuaValueToGo converts a Lua value to a Go value.
func convertLuaValueToGo(v lua.LValue) any {
	switch val := v.(type) {
	case *lua.LNilType:
		return nil
	case lua.LBool:
		return bool(val)
	case lua.LNumber:
		return float64(val)
	case lua.LString:
		return string(val)
	case *lua.LTable:
		return convertLuaTable(val)
	default:
		return val.String()
	}
}

// LoadAndRegisterLuaTools loads Lua tools from the user's global directory
// (~/.tyci/tools) and registers them. It runs from this package's init(),
// which fires before main() gets a chance to decide anything — so it must
// never touch project-local ./.tyci/tools. That directory is loaded
// separately, by LoadAndRegisterLocalLuaTools, only once the caller has
// decided (internal/trust) that the current project is trusted: a Lua tool
// can shell out via ctx.run, exactly the kind of project-supplied code item
// 23's trust prompt exists to gate.
func LoadAndRegisterLuaTools() {
	registerLuaToolsFromDir(filepath.Join(os.Getenv("HOME"), ".tyci", "tools"))
}

// LoadAndRegisterLocalLuaTools loads and registers Lua tools from a
// project-local directory (normally "<project>/.tyci/tools"). Callers must
// only invoke this once the project has been decided trusted — see
// LoadAndRegisterLuaTools's doc comment.
func LoadAndRegisterLocalLuaTools(dir string) {
	registerLuaToolsFromDir(dir)
}

func registerLuaToolsFromDir(dir string) {
	tools, err := LoadLuaTools(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load lua tools from %s: %v\n", dir, err)
		return
	}

	for _, tool := range tools {
		// Check for name collision
		if _, exists := toolRegistry[tool.name]; exists {
			fmt.Fprintf(os.Stderr, "Warning: lua tool %s conflicts with built-in tool, skipping\n", tool.name)
			continue
		}
		toolRegistry[tool.name] = tool
	}
}
