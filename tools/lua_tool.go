package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

// LuaTool implements the Tool interface for user-defined Lua scripts.
type LuaTool struct {
	name        string
	description string
	parameters  map[string]any
	runFunc     *lua.LFunction
	scriptPath  string
}

// Name returns the tool name.
func (t *LuaTool) Name() string {
	return t.name
}

// Run executes the Lua tool with the given input.
func (t *LuaTool) Run(ctx context.Context, input map[string]any) ToolResult {
	L := lua.NewState()
	defer L.Close()

	// Set up sandboxed context
	sandbox := newLuaContext(ctx, L)
	L.SetGlobal("ctx", sandbox)

	// Load and execute the script
	if err := L.DoFile(t.scriptPath); err != nil {
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

// loadLuaTool loads a single Lua tool from a script file.
func loadLuaTool(scriptPath string) (*LuaTool, error) {
	L := lua.NewState()
	defer L.Close()

	if err := L.DoFile(scriptPath); err != nil {
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

	// Get run function
	runVal := table.RawGetString("run")
	if runVal.Type() != lua.LTFunction {
		return nil, fmt.Errorf("lua script table must have a 'run' function")
	}

	return &LuaTool{
		name:        name,
		description: description,
		parameters:  params,
		runFunc:     runVal.(*lua.LFunction),
		scriptPath:  scriptPath,
	}, nil
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

// LoadAndRegisterLuaTools loads Lua tools from directories and registers them.
func LoadAndRegisterLuaTools() {
	dirs := []string{
		filepath.Join(os.Getenv("HOME"), ".tyci", "tools"),
		".tyci/tools",
	}

	for _, dir := range dirs {
		tools, err := LoadLuaTools(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load lua tools from %s: %v\n", dir, err)
			continue
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
}
