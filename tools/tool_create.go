package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
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

// validToolName reports whether name is a name that tool_create accepts.
func validToolName(name string) bool {
	return toolCreateNameRE.MatchString(name) && !strings.HasPrefix(name, "mcp_")
}

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
	if !validToolName(name) {
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

// sessionFolderPrefix starts the name of every session folder. The name holds
// the PID of the tyci process that owns the folder, see RemoveStaleSessionFolders.
const sessionFolderPrefix = "tyci-session-"

// staleSessionAge is the age after which a session folder without a PID in its
// name (made by an older tyci) is removed.
const staleSessionAge = 24 * time.Hour

// NewSessionFolder makes the run folder of a main chat session. It returns the
// folder and an end func. The end func unregisters the tools made in the
// folder and removes the folder. Call it when the session ends.
func NewSessionFolder() (string, func(), error) {
	dir, err := os.MkdirTemp("", fmt.Sprintf("%s%d-", sessionFolderPrefix, os.Getpid()))
	if err != nil {
		return "", nil, err
	}
	end := func() {
		RemoveRunTools(dir)
		_ = os.RemoveAll(dir)
	}
	return dir, end, nil
}

// ReloadRunTools registers again the Lua tools that tool_create made in the run
// folder dir, from the files in dir/tools. A paused run calls it when it starts
// again, also after a tyci restart, because the registry is empty after a
// restart. A file that fails a check is reported through warn and skipped. The
// other files load as usual. A nil warn writes to stderr. A run folder without a
// tools folder is left alone.
func ReloadRunTools(dir string, warn func(string)) {
	if warn == nil {
		warn = func(msg string) { fmt.Fprintln(os.Stderr, msg) }
	}
	toolsDir := filepath.Join(dir, "tools")
	entries, err := os.ReadDir(toolsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			warn(fmt.Sprintf("Warning: cannot read the Lua tools of this run: %v", err))
		}
		return
	}
	var loaded []*LuaTool
	for _, e := range entries {
		stem, isLua := strings.CutSuffix(e.Name(), ".lua")
		if e.IsDir() || !isLua {
			continue
		}
		path := filepath.Join(toolsDir, e.Name())
		tool, err := loadRunToolFile(stem, path)
		if err != nil {
			warn(fmt.Sprintf("Warning: skipping the Lua tool %s of this run: %v", path, err))
			continue
		}
		loaded = append(loaded, tool)
	}
	toolRegistryMu.Lock()
	defer toolRegistryMu.Unlock()
	installLuaTools(dir, loaded)
}

// loadRunToolFile loads the script at path, a tool made with tool_create under
// the name stem. It runs the same checks as tool_create: the name rule, the
// bounded load, and a schema name equal to the name.
func loadRunToolFile(stem, path string) (*LuaTool, error) {
	if !validToolName(stem) {
		return nil, fmt.Errorf("name %q is not valid", stem)
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolCreateLimit)
	defer cancel()
	tool, err := loadLuaToolCtx(ctx, path)
	if err != nil {
		return nil, err
	}
	if tool.name != stem {
		return nil, fmt.Errorf("schema.name is %q, but the file name is %q", tool.name, stem)
	}
	return tool, nil
}

// RemoveStaleSessionFolders removes the session folders in root that no live
// process owns, and returns how many it removed. A folder name holds the PID of
// its owner. A folder whose PID does not exist is removed. A PID that exists, also
// one of another user, keeps the folder. A folder without a PID is removed when
// it is older than 24 hours. Other entries are not touched.
func RemoveStaleSessionFolders(root string) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), sessionFolderPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || !sessionFolderStale(e.Name(), info) {
			continue
		}
		if os.RemoveAll(filepath.Join(root, e.Name())) == nil {
			removed++
		}
	}
	return removed
}

// sessionFolderStale reports whether the session folder name, with info, has no
// live owner. See RemoveStaleSessionFolders.
func sessionFolderStale(name string, info fs.FileInfo) bool {
	pidText, _, hasPID := strings.Cut(strings.TrimPrefix(name, sessionFolderPrefix), "-")
	pid, err := strconv.Atoi(pidText)
	if hasPID && err == nil {
		return pid > 0 && errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}
	return time.Since(info.ModTime()) > staleSessionAge
}
