package gitinfo

import (
	"os"
	"path/filepath"
	"testing"
)

// resolved returns dir with symlinks resolved, because ProjectRoot answers
// with the resolved path (macOS puts temp dirs behind /var -> /private/var).
func resolved(t *testing.T, dir string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// writeFile creates path (and its parents) with content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProjectFileNormalRepo(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(resolved(t, dir), ".tyci", "config.json"), "{}")

	want := filepath.Join(resolved(t, dir), ".tyci", "config.json")
	if got := ProjectFile(dir, "config.json"); got != want {
		t.Fatalf("ProjectFile = %q, want %q", got, want)
	}
	if got := ProjectFile(dir, "hooks.json"); got != "" {
		t.Fatalf("ProjectFile(missing) = %q, want empty", got)
	}
}

func TestProjectFileLinkedWorktreeFallsBackToMain(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(resolved(t, dir), ".tyci", "hooks.json"), "{}")
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, dir, "worktree", "add", "-q", "-b", "tyci/child", wt)
	t.Cleanup(func() { run(t, dir, "worktree", "remove", "--force", wt) })

	if got := ProjectFile(wt, "hooks.json"); got != filepath.Join(resolved(t, dir), ".tyci", "hooks.json") {
		t.Fatalf("ProjectFile(worktree) = %q, want the main checkout file", got)
	}
}

func TestProjectFilePerEntryWorktreeWins(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(resolved(t, dir), ".tyci", "config.json"), "main")
	writeFile(t, filepath.Join(resolved(t, dir), ".tyci", "hooks.json"), "main")
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, dir, "worktree", "add", "-q", "-b", "tyci/child", wt)
	t.Cleanup(func() { run(t, dir, "worktree", "remove", "--force", wt) })
	writeFile(t, filepath.Join(resolved(t, wt), ".tyci", "workflows", "w", "workflow.json"), "{}")

	// The worktree has .tyci/ but no config.json: the main file still loads.
	if got := ProjectFile(wt, "config.json"); got != filepath.Join(resolved(t, dir), ".tyci", "config.json") {
		t.Fatalf("config.json = %q, want main", got)
	}
	writeFile(t, filepath.Join(resolved(t, wt), ".tyci", "config.json"), "wt")
	if got := ProjectFile(wt, "config.json"); got != filepath.Join(wt, ".tyci", "config.json") {
		t.Fatalf("config.json = %q, want worktree", got)
	}
}

func TestProjectFileFromSubdirectory(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(resolved(t, dir), ".tyci", "config.json"), "{}")
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ProjectFile(sub, "config.json"); got != filepath.Join(resolved(t, dir), ".tyci", "config.json") {
		t.Fatalf("ProjectFile(subdir) = %q", got)
	}
	if got := ProjectDir(sub); got != resolved(t, dir) {
		t.Fatalf("ProjectDir(subdir) = %q, want %q", got, dir)
	}
}

func TestProjectFileBareRepoFallsBackToWd(t *testing.T) {
	if _, err := os.Stat(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(t.TempDir(), "x.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, bare, "init", "-q", "--bare", ".")
	writeFile(t, filepath.Join(resolved(t, bare), ".tyci", "config.json"), "{}")

	if got := ProjectDir(bare); got != bare {
		t.Fatalf("ProjectDir(bare) = %q, want %q", got, bare)
	}
	if got := ProjectFile(bare, "config.json"); got != filepath.Join(bare, ".tyci", "config.json") {
		t.Fatalf("ProjectFile(bare) = %q", got)
	}
}

func TestProjectFileSubmoduleUsesOwnRoot(t *testing.T) {
	dir := initRepo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, sub, "init", "-q", "-b", "main")
	// A submodule has a ".git" file, like a linked worktree, but its git dir
	// is under .git/modules, not .git/worktrees.
	gitDir := filepath.Join(dir, ".git", "modules", "sub")
	if err := os.MkdirAll(filepath.Dir(gitDir), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, sub, "init", "-q", "--separate-git-dir", gitDir, ".")
	writeFile(t, filepath.Join(resolved(t, sub), ".tyci", "config.json"), "{}")
	writeFile(t, filepath.Join(resolved(t, dir), ".tyci", "config.json"), "{}")

	if got := ProjectDir(sub); got != resolved(t, sub) {
		t.Fatalf("ProjectDir(submodule) = %q, want %q", got, resolved(t, sub))
	}
	if got := ProjectFile(sub, "config.json"); got != filepath.Join(resolved(t, sub), ".tyci", "config.json") {
		t.Fatalf("ProjectFile(submodule) = %q, want the submodule file", got)
	}
}

func TestProjectFileSymlinkedTyciIgnored(t *testing.T) {
	dir := initRepo(t)
	elsewhere := t.TempDir()
	writeFile(t, filepath.Join(elsewhere, "config.json"), "{}")
	if err := os.Symlink(elsewhere, filepath.Join(resolved(t, dir), ".tyci")); err != nil {
		t.Fatal(err)
	}
	if got := ProjectFile(dir, "config.json"); got != "" {
		t.Fatalf("ProjectFile through symlinked .tyci = %q, want empty", got)
	}
}

func TestProjectFileSymlinkedEntryIgnored(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "real.json"), "{}")
	if err := os.MkdirAll(filepath.Join(resolved(t, dir), ".tyci"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "real.json"), filepath.Join(resolved(t, dir), ".tyci", "hooks.json")); err != nil {
		t.Fatal(err)
	}
	if got := ProjectFile(dir, "hooks.json"); got != "" {
		t.Fatalf("ProjectFile(symlink entry) = %q, want empty", got)
	}
}

func TestProjectFileDeletedMainFallsBackToWorktree(t *testing.T) {
	dir := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, dir, "worktree", "add", "-q", "-b", "tyci/child", wt)
	writeFile(t, filepath.Join(resolved(t, wt), ".tyci", "config.json"), "{}")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if got := ProjectFile(wt, "config.json"); got != filepath.Join(wt, ".tyci", "config.json") {
		t.Fatalf("ProjectFile(deleted main) = %q, want worktree file", got)
	}
	if got := ProjectDir(wt); got != wt {
		t.Fatalf("ProjectDir(deleted main) = %q, want the worktree %q", got, wt)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the deleted main checkout must not be re-created: %v", err)
	}
}

func TestProjectFileOutsideRepository(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(resolved(t, dir), ".tyci", "config.json"), "{}")
	if got := ProjectFile(dir, "config.json"); got != filepath.Join(dir, ".tyci", "config.json") {
		t.Fatalf("ProjectFile(no repo) = %q", got)
	}
	if got := ProjectDir(dir); got != dir {
		t.Fatalf("ProjectDir(no repo) = %q, want %q", got, dir)
	}
}

func TestProjectPathFallsBackToMainCheckout(t *testing.T) {
	dir := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, dir, "worktree", "add", "-q", "-b", "tyci/child", wt)
	t.Cleanup(func() { run(t, dir, "worktree", "remove", "--force", wt) })

	want := filepath.Join(resolved(t, dir), ".tyci", "memory")
	if got := ProjectPath(wt, "memory"); got != want {
		t.Fatalf("ProjectPath(missing) = %q, want %q", got, want)
	}
	writeFile(t, filepath.Join(wt, ".tyci", "memory", "n.md"), "x")
	if got := ProjectPath(wt, "memory"); got != filepath.Join(wt, ".tyci", "memory") {
		t.Fatalf("ProjectPath(worktree has it) = %q", got)
	}
}
