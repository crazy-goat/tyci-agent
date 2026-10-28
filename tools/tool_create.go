package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// toolCreateNameRE is the shape of a tool name. The name is also the file name
// in the run folder, so a name with a slash or a dot is refused.
var toolCreateNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func (t *ToolCreateTool) Name() string { return toolCreateName }

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
	if _, exists := lookupTool(name); exists {
		return failf("a tool named %q already exists; choose another name", name)
	}

	toolsDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		return failf("cannot create the tools folder of this run: %v", err)
	}
	path := filepath.Join(toolsDir, name+".lua")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		return failf("cannot write %s: %v", path, err)
	}
	// loadLuaTool compiles the script and runs it once, as a file tool is
	// loaded. A script that does not load is removed, so nothing is left behind.
	tool, err := loadLuaTool(path)
	if err == nil && tool.name != name {
		err = fmt.Errorf("schema.name is %q, but name is %q", tool.name, name)
	}
	if err != nil {
		_ = os.Remove(path)
		return failf("the Lua tool does not load: %v", err)
	}

	// The check and the insert are one critical section, so a tool with the
	// same name cannot be registered in between.
	toolRegistryMu.Lock()
	defer toolRegistryMu.Unlock()
	if _, exists := toolRegistry[name]; exists {
		_ = os.Remove(path)
		return failf("a tool named %q already exists; choose another name", name)
	}
	toolRegistry[name] = tool
	luaToolsByDir[dir] = append(luaToolsByDir[dir], name)
	return ToolResult{
		Type:    "result",
		Success: true,
		Content: fmt.Sprintf("Created the tool %q. Call it with the lua tool: tool(%q, {...}). It lasts until this run or session ends.", name, name),
	}
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
