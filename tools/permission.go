package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Permission modes of a workflow role. The values match flowconfig.Role.
const (
	PermissionReadOnly      = "read-only"
	PermissionWorktreeWrite = "worktree-write"
	PermissionFull          = "full"
)

// Policy is the tool policy of one workflow role run. It is built from the
// role in flowconfig and enforced in Go. A prompt cannot change it.
type Policy struct {
	// Role is the role name. It appears in refusals only.
	Role string
	// Permission is PermissionReadOnly, PermissionWorktreeWrite or PermissionFull.
	Permission string
	// Network is false when the role may not use web or MCP tools.
	Network bool
	// Tools is the explicit tool list of the role. Nil means the default set of Permission.
	Tools []string
	// Dir is the worktree of the run. With worktree-write, write and edit stay inside it.
	Dir string
}

// readTools are the tools of the read-only mode. web is here too: it reads
// and does not change the worktree, and Network turns it off.
var readTools = []string{"read", "find", "skills", "agents", "todo", "wait", "ask_parent", "message", "web"}

// writeTools are added by worktree-write.
var writeTools = []string{"write", "bash", "kill_job"}

// isMCPTool reports whether name is an MCP tool (mcp_<server>_<tool>).
func isMCPTool(name string) bool {
	return strings.HasPrefix(name, "mcp_")
}

// modeAllows reports whether the permission mode gives the built-in tool name by default.
func modeAllows(permission, name string) bool {
	switch permission {
	case PermissionFull:
		return true
	case PermissionWorktreeWrite:
		return slices.Contains(readTools, name) || slices.Contains(writeTools, name)
	default:
		return slices.Contains(readTools, name)
	}
}

// AllowedFor reports whether the policy lets the role call the tool name.
// The always-allowed tools (help, lua, report_progress) are always allowed.
// An explicit Tools list adds MCP tools, but it cannot widen the permission
// mode: a built-in tool outside the mode is refused even when it is listed.
func AllowedFor(p Policy, name string) bool {
	if slices.Contains(alwaysAllowedTools, name) {
		return true
	}
	if isMCPTool(name) {
		// One rule, the same as ToolNames: an MCP tool is offered only when
		// the list names it, or when there is no list, the mode is full and
		// the network is on.
		if mcpListed(p.Tools, name) {
			return true
		}
		return p.Tools == nil && p.Permission == PermissionFull && p.Network
	}
	if name == "web" && !p.Network {
		return false
	}
	if p.Tools != nil && !slices.Contains(p.Tools, name) {
		return false
	}
	return modeAllows(p.Permission, name)
}

// mcpListed reports whether an explicit tool list names the MCP tool name,
// literally or through an mcp_<server>_* wildcard.
func mcpListed(tools []string, name string) bool {
	for _, t := range tools {
		if t == name || mcpAllowedByWildcard(name, []string{t}) {
			return true
		}
	}
	return false
}

// ToolNames returns the names for the subagent schema and runtime gate of the
// run. Nil means no restriction. The result is never empty when restricted,
// because an empty list would mean no restriction to the subagent builders.
func (p Policy) ToolNames() []string {
	if p.Tools == nil && p.Permission == PermissionFull && p.Network {
		return nil
	}
	var base []string
	switch {
	case p.Tools != nil:
		base = p.Tools
	case p.Permission == PermissionFull:
		base = allSchemaNames()
	default:
		base = append(append([]string{}, readTools...), writeTools...)
	}
	out := []string{}
	for _, n := range base {
		if AllowedFor(p, n) && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		out = []string{"help"}
	}
	return out
}

// allSchemaNames returns the names of the tools in the subagent schema, without
// web and MCP tools when the policy has no network. It is used for a full
// role that has no explicit tool list.
func allSchemaNames() []string {
	var names []string
	for _, s := range GetSubagentToolsSchema() {
		fn, ok := s["function"].(map[string]any)
		if !ok {
			continue
		}
		if name, ok := fn["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

type policyCtxKey struct{}

// WithPolicy returns a context in which the tools of p are checked. It adds a
// ToolGate that refuses a disallowed call in RunTool, and it stores p so that
// write checks the worktree root. Gates nest, so a child can only narrow it.
func WithPolicy(ctx context.Context, p Policy) context.Context {
	ctx = context.WithValue(ctx, policyCtxKey{}, p)
	return WithToolGate(ctx, func(name string) error {
		if AllowedFor(p, name) {
			return nil
		}
		return fmt.Errorf("tool %q is not allowed for role %q (permission %s)", name, p.Role, p.Permission)
	})
}

// checkWriteRoot refuses a write or edit path outside the worktree of the
// policy. It applies only with worktree-write. Symlinks are resolved first.
func checkWriteRoot(ctx context.Context, path string) error {
	p, ok := ctx.Value(policyCtxKey{}).(Policy)
	if !ok || p.Permission != PermissionWorktreeWrite || p.Dir == "" {
		return nil
	}
	if within(p.Dir, path) {
		return nil
	}
	return fmt.Errorf("path %q is outside the worktree of role %q (permission %s)", path, p.Role, p.Permission)
}

// within reports whether path, after symlinks are resolved, is root or below it.
// The path may not exist yet: the longest existing prefix is resolved, and the
// rest is appended to it.
func within(root, path string) bool {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	existing := abs
	for {
		if real, err := filepath.EvalSymlinks(existing); err == nil {
			rest, err := filepath.Rel(existing, abs)
			if err != nil {
				return false
			}
			full := filepath.Join(real, rest)
			rel, err := filepath.Rel(realRoot, full)
			return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return false
		}
		existing = parent
	}
}
