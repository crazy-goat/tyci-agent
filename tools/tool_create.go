package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ToolCreateTool implements "tool_create": the model writes a Lua tool while it
// works. The script is the same kind as a file in .tyci/tools (see
// loadLuaTool). It is written into the run folder, so it is kept with the run,
// and it is registered at once, so the tool is callable with the lua tool
// until the run or the session ends.
type ToolCreateTool struct{}

// toolCreateName is the name of the tool. Permission and schema code refer to
// it: a role gets the tool only when its tools list names it (see AllowedFor).
const toolCreateName = "tool_create"

// roleGatedTools are the tools that only the main chat and a role or agent
// that names them in its tools list may call (see AllowedFor and
// SubagentToolCreateGate).
var roleGatedTools = []string{toolCreateName, toolPromoteName}

// isRoleGatedTool reports whether name is one of roleGatedTools.
func isRoleGatedTool(name string) bool {
	return slices.Contains(roleGatedTools, name)
}

// toolCreateLimit bounds the load of the script and each supplied test. The
// script is model-written, so it must not run without a time limit.
const toolCreateLimit = 2 * time.Second

// toolCreateNameRE is the shape of a tool name. The name is also the file name
// in the run folder, so a name with a slash or a dot is refused.
var toolCreateNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func (t *ToolCreateTool) Name() string { return toolCreateName }

// toolCreateCase is one supplied test: the args to run the tool with, and a
// substring that the result content must contain.
type toolCreateCase struct {
	args   map[string]any
	expect string
}

func (t *ToolCreateTool) Run(ctx context.Context, input map[string]any) ToolResult {
	dir := RunDirFrom(ctx)
	if dir == "" {
		return failf("tool_create needs a run folder, and this agent has none. Only the main chat and a workflow run have one")
	}
	name := strings.TrimSpace(stringParam(input, "name", ""))
	source := stringParam(input, "source", "")
	if !toolCreateNameRE.MatchString(name) || strings.HasPrefix(name, "mcp_") {
		return validationf("name %q is not valid; use 1-64 lowercase letters, digits and underscores, starting with a letter, and not starting with mcp_", name)
	}
	if strings.TrimSpace(source) == "" {
		return validationf("source is required: a Lua script that returns a table with schema and run")
	}
	cases, err := toolCreateTestCases(input)
	if err != nil {
		return validationf("%v", err)
	}
	if _, exists := lookupTool(name); exists {
		return failf("a tool named %q already exists; choose another name", name)
	}

	toolsDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		return failf("cannot create the tools folder of this run: %v", err)
	}
	// The script is written to a temp file of its own, so two calls with one
	// name never write the same file. The temp file is renamed into place only
	// after the registry insert succeeds. Every other exit removes it.
	f, err := os.CreateTemp(toolsDir, name+".*.tmp")
	if err != nil {
		return failf("cannot write the tool of this run: %v", err)
	}
	tmpPath := f.Name()
	fail := func(format string, args ...any) ToolResult {
		_ = os.Remove(tmpPath)
		return failf(format, args...)
	}
	if _, err := f.WriteString(source); err != nil {
		_ = f.Close()
		return fail("cannot write the tool of this run: %v", err)
	}
	if err := f.Close(); err != nil {
		return fail("cannot write the tool of this run: %v", err)
	}

	// loadLuaTool compiles the script and runs it once, as a file tool is
	// loaded. The load and the tests run under a time limit, so a script that
	// never returns fails here and does not hang the turn.
	loadCtx, cancelLoad := context.WithTimeout(ctx, toolCreateLimit)
	defer cancelLoad()
	tool, err := loadLuaToolCtx(loadCtx, tmpPath)
	if err == nil && tool.name != name {
		err = fmt.Errorf("schema.name is %q, but name is %q", tool.name, name)
	}
	if err != nil {
		return fail("the Lua tool does not load: %v", err)
	}
	testCtx, cancelTests := context.WithTimeout(ctx, toolCreateLimit)
	defer cancelTests()
	if err := runToolTests(testCtx, tool, cases); err != nil {
		return fail("a test of the Lua tool failed: %v", err)
	}

	final := filepath.Join(toolsDir, name+".lua")
	// The check and the insert are one critical section, so a tool with the
	// same name cannot be registered in between.
	toolRegistryMu.Lock()
	defer toolRegistryMu.Unlock()
	if _, exists := toolRegistry[name]; exists {
		return fail("a tool named %q already exists; choose another name", name)
	}
	tool.scriptPath = final
	toolRegistry[name] = tool
	if err := os.Rename(tmpPath, final); err != nil {
		delete(toolRegistry, name)
		return fail("cannot save %s: %v", final, err)
	}
	luaToolsByDir[dir] = append(luaToolsByDir[dir], name)
	return ToolResult{
		Type:    "result",
		Success: true,
		Content: fmt.Sprintf("Created the tool %q. Call it with the lua tool: tool(%q, {...}). It lasts until this run or session ends.", name, name),
	}
}

// toolCreateTestCases reads the optional tests input: a list of objects with
// args (optional) and expect (required).
func toolCreateTestCases(input map[string]any) ([]toolCreateCase, error) {
	raw := input["tests"]
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("tests must be a list of objects with args and expect")
	}
	cases := make([]toolCreateCase, 0, len(list))
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tests[%d] must be an object", i)
		}
		expect, ok := obj["expect"].(string)
		if !ok || expect == "" {
			return nil, fmt.Errorf("tests[%d].expect must be a non-empty string", i)
		}
		args := map[string]any{}
		if a := obj["args"]; a != nil {
			m, ok := a.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("tests[%d].args must be an object", i)
			}
			args = m
		}
		cases = append(cases, toolCreateCase{args: args, expect: expect})
	}
	return cases, nil
}

// runToolTests runs each case against tool and returns the first failure. A
// case passes when the tool succeeds and its content contains expect.
func runToolTests(ctx context.Context, tool *LuaTool, cases []toolCreateCase) error {
	for i, c := range cases {
		res := tool.Run(ctx, c.args)
		if !res.Success {
			return fmt.Errorf("test %d: the tool failed: %s", i+1, res.Error)
		}
		if !strings.Contains(res.Content, c.expect) {
			return fmt.Errorf("test %d: the result %q does not contain %q", i+1, res.Content, c.expect)
		}
	}
	return nil
}

// RemoveRunTools unregisters the Lua tools that tool_create made in dir. Call
// it when the run or the session that owns dir ends. The files stay in dir.
func RemoveRunTools(dir string) {
	toolRegistryMu.Lock()
	defer toolRegistryMu.Unlock()
	for _, name := range luaToolsByDir[dir] {
		delete(toolRegistry, name)
	}
	delete(luaToolsByDir, dir)
}

// NewSessionFolder makes the run folder of a main chat session. It returns the
// folder and an end func. The end func unregisters the tools made in the
// folder and removes the folder. Call it when the session ends.
func NewSessionFolder() (string, func(), error) {
	dir, err := os.MkdirTemp("", "tyci-session-")
	if err != nil {
		return "", nil, err
	}
	end := func() {
		RemoveRunTools(dir)
		_ = os.RemoveAll(dir)
	}
	return dir, end, nil
}
