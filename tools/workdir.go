package tools

import (
	"context"
	"path/filepath"
)

// Per-call working directory.
//
// Every file tool in this package used to resolve a relative path against the
// process working directory, which is fine for one agent and wrong the moment
// several run at once: children are goroutines in this process, so os.Chdir is
// not available to them — it would move the ground under every other agent at
// the same time.
//
// So the working directory travels in the context instead. A child given its
// own git worktree (see subagent's isolation option) carries that directory
// here, and read, write, find and bash all resolve against it without knowing
// why. Nothing set means the process directory, which is what every existing
// call site already assumed.
type workdirCtxKey struct{}

// WithWorkdir returns a context whose file operations resolve relative paths
// against dir. An empty dir is a no-op, so callers need not special-case it.
func WithWorkdir(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, workdirCtxKey{}, dir)
}

// Workdir returns the directory this call should treat as its working
// directory, or "" when it is the process's own.
func Workdir(ctx context.Context) string {
	dir, _ := ctx.Value(workdirCtxKey{}).(string)
	return dir
}

// resolvePath makes a tool's path argument absolute with respect to the
// context's working directory.
//
// An absolute path is returned unchanged: a child working in a worktree is
// still allowed to read /etc/hosts or a file in the parent's checkout, and
// silently rewriting an absolute path would be a surprise nobody could debug.
// Confining a child to its worktree is a separate question from resolving
// relative paths, and conflating them here would make both harder to reason
// about.
func resolvePath(ctx context.Context, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	dir := Workdir(ctx)
	if dir == "" {
		return path
	}
	return filepath.Join(dir, path)
}
