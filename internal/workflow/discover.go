package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/decodo/tyci/internal/agentdefs"
)

// ResolveScript finds a Lua orchestration script by name, following the same
// project/global discovery convention used elsewhere in the codebase:
//
//   - Lua *tools* live under ~/.tyci/tools (global) and <project>/.tyci/tools
//     (project-local), project-local winning — see tools/lua_tool.go's
//     LoadAndRegisterLuaTools / LoadAndRegisterLocalLuaTools.
//   - Named *agent* definitions (markdown, *.md) live under ~/.tyci/agents
//     (global) and <project>/.tyci/agents (project-local), project-local
//     winning — see internal/agentdefs.Dirs.
//
// Workflow scripts reuse that second directory (agentdefs.Dirs) rather than
// inventing a third convention: a Lua orchestration script IS an agent
// definition, just one that drives its own sessions instead of only
// supplying a system prompt. agentdefs.LoadDir only reads *.md files, so a
// *.lua script sitting alongside markdown agent definitions in the same
// directory causes no collision.
//
// name may be a bare script name ("triage", resolved to "triage.lua" in one
// of the agents directories, project-local taking precedence over global on
// a name collision) or a path to an existing .lua file, which is used
// directly without going through the agents directories at all.
//
// ResolveScript always searches both the global and project-local agents
// directories (agentdefs.Dirs(wd)); it does not know about trust and must
// not be used directly on a caller's behalf when the project-local
// directory should be excluded from an untrusted project (see
// ResolveScriptIn, and workflowcmd.go's trust.Decide gate, which is what
// actually makes that call for the CLI).
func ResolveScript(wd, name string) (string, error) {
	return ResolveScriptIn(agentdefs.Dirs(wd), name)
}

// ResolveScriptIn is ResolveScript's underlying search, parameterized on the
// candidate directories explicitly rather than deriving them from wd. Later
// entries in dirs win on a name collision, mirroring agentdefs.Load's merge
// order. Passing a single directory (e.g. just the global one) is how a
// caller excludes the project-local directory — see workflowcmd.go, which
// does exactly that for an untrusted project.
func ResolveScriptIn(dirs []string, name string) (string, error) {
	if strings.HasSuffix(name, ".lua") {
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			return name, nil
		}
	}

	for i := len(dirs) - 1; i >= 0; i-- {
		candidate := filepath.Join(dirs[i], name+".lua")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("workflow script %q not found in %s", name, strings.Join(dirs, " or "))
}

// ListScripts returns the names (without the .lua suffix) of every workflow
// script visible from wd: global first, then project-local, with a
// project-local script overriding a same-named global one — the same merge
// order as agentdefs.List. Like ResolveScript, it always includes the
// project-local directory; see ListScriptsIn for the trust-aware form.
func ListScripts(wd string) []string {
	return ListScriptsIn(agentdefs.Dirs(wd))
}

// ListScriptsIn is ListScripts's underlying scan, parameterized on the
// candidate directories explicitly — see ResolveScriptIn's doc comment for
// why this split exists.
func ListScriptsIn(dirs []string) []string {
	present := make(map[string]bool)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lua") {
				continue
			}
			present[strings.TrimSuffix(entry.Name(), ".lua")] = true
		}
	}

	names := make([]string, 0, len(present))
	for name := range present {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
