package tools

// Per-call working directory. Children are goroutines in this process, so
// os.Chdir is not available to them — it would move the ground under every
// other agent running at the same time. The directory therefore travels in the
// context, and the file tools resolve against it.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathUsesTheContextWorkdir(t *testing.T) {
	ctx := WithWorkdir(context.Background(), "/somewhere/else")

	if got := resolvePath(ctx, "internal/x.go"); got != "/somewhere/else/internal/x.go" {
		t.Errorf("got %q", got)
	}
	// An absolute path is left alone: a child in a worktree may still read a
	// file outside it, and silently rewriting the path would be undebuggable.
	if got := resolvePath(ctx, "/etc/hosts"); got != "/etc/hosts" {
		t.Errorf("an absolute path was rewritten to %q", got)
	}
	// No workdir set means the process's own, i.e. leave the path as it was.
	if got := resolvePath(context.Background(), "rel/x.go"); got != "rel/x.go" {
		t.Errorf("got %q", got)
	}
}

func TestWithWorkdirIgnoresAnEmptyDir(t *testing.T) {
	ctx := WithWorkdir(context.Background(), "")
	if got := Workdir(ctx); got != "" {
		t.Fatalf("got %q", got)
	}
}

// TestReadHonoursTheWorkdir and the three tests after it are the point of the
// whole mechanism: the tools have to agree on which file a relative path means.
func TestReadHonoursTheWorkdir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("from the worktree"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := RunTool(WithWorkdir(context.Background(), dir), "read", map[string]any{"path": "note.txt"})
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if !strings.Contains(res.Content, "from the worktree") {
		t.Fatalf("read the wrong file: %q", res.Content)
	}
}

func TestWriteHonoursTheWorkdir(t *testing.T) {
	dir := t.TempDir()

	res := RunTool(WithWorkdir(context.Background(), dir), "write",
		map[string]any{"path": "new.txt", "content": "hello"})
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatalf("the file did not land in the workdir: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("got %q", data)
	}
}

// TestWriteFreshnessAgreesWithTheWorkdir: the freshness stamps are keyed by
// path, so a read and a write of the same relative name must resolve
// identically or every edit in a worktree would be refused as unread.
func TestWriteFreshnessAgreesWithTheWorkdir(t *testing.T) {
	ResetFileStamps()
	dir := t.TempDir()
	target := filepath.Join(dir, "edit.txt")
	if err := os.WriteFile(target, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := WithWorkdir(context.Background(), dir)
	if res := RunTool(ctx, "read", map[string]any{"path": "edit.txt"}); !res.Success {
		t.Fatalf("read: %s", res.Error)
	}
	res := RunTool(ctx, "write", map[string]any{
		"path": "edit.txt", "oldString": "before", "newString": "after",
	})
	if !res.Success {
		t.Fatalf("the edit was refused even though the same relative path was read: %s", res.Error)
	}
}

func TestFindHonoursTheWorkdir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "only-here.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := RunTool(WithWorkdir(context.Background(), dir), "find",
		map[string]any{"method": "glob", "pattern": "**/*.go"})
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if !strings.Contains(res.Content, "only-here.go") {
		t.Fatalf("find searched the wrong directory: %q", res.Content)
	}
}

func TestBashHonoursTheWorkdir(t *testing.T) {
	dir := t.TempDir()

	res := RunTool(WithWorkdir(context.Background(), dir), "bash",
		map[string]any{"command": "pwd", "description": "where am I"})
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	// Compared against both spellings: exec.Cmd.Dir is used verbatim, and on
	// macOS /var is a symlink to /private/var, so pwd may print either
	// depending on how the shell got there.
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(res.Content)
	if !strings.Contains(got, dir) && !strings.Contains(got, real) {
		t.Fatalf("the command ran in the wrong directory: %q (want %q or %q)", got, dir, real)
	}
}

// TestLuaScriptInheritsTheWorkdir: a script's tool() calls go through RunTool
// with the script's own context, so they inherit this for free — worth pinning,
// because it is the one path that could have bypassed it.
func TestLuaScriptInheritsTheWorkdir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seen.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := RunTool(WithWorkdir(context.Background(), dir), "lua",
		map[string]any{"script": `return tool("read", {path = "seen.txt"}).content`})
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if !strings.Contains(res.Content, "inside") {
		t.Fatalf("the script read outside its workdir: %q", res.Content)
	}
}
