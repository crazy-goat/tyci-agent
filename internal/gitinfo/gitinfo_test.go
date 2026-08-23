package gitinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// fresh clears the TTL cache so each case reads the filesystem it just built.
func fresh(t *testing.T) {
	t.Helper()
	mu.Lock()
	cache = map[string]entry{}
	mu.Unlock()
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// initRepo builds a repository with one commit on branch "main".
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "f")
	run(t, dir, "commit", "-qm", "one")
	return dir
}

func TestBranchOnRepoRoot(t *testing.T) {
	fresh(t)
	dir := initRepo(t)
	if got := Branch(dir); got != "main" {
		t.Fatalf("Branch = %q, want main", got)
	}
}

func TestBranchFromSubdirectory(t *testing.T) {
	fresh(t)
	dir := initRepo(t)
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Branch(sub); got != "main" {
		t.Fatalf("Branch = %q, want main", got)
	}
}

func TestBranchSlashedName(t *testing.T) {
	fresh(t)
	dir := initRepo(t)
	run(t, dir, "checkout", "-q", "-b", "feature/nested/name")
	if got := Branch(dir); got != "feature/nested/name" {
		t.Fatalf("Branch = %q, want feature/nested/name", got)
	}
}

// A linked worktree keeps its own HEAD behind a .git *file*; reporting the
// parent's branch here would be the bug this package exists to avoid.
func TestBranchInLinkedWorktree(t *testing.T) {
	fresh(t)
	dir := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, dir, "worktree", "add", "-q", "-b", "tyci/child", wt)
	t.Cleanup(func() { run(t, dir, "worktree", "remove", "--force", wt) })

	if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || fi.IsDir() {
		t.Fatalf(".git in worktree should be a file: %v", err)
	}
	if got := Branch(wt); got != "tyci/child" {
		t.Fatalf("Branch = %q, want tyci/child", got)
	}
	// The parent is unaffected.
	fresh(t)
	if got := Branch(dir); got != "main" {
		t.Fatalf("parent Branch = %q, want main", got)
	}
}

// ProjectRoot must resolve a linked worktree back to the main repository so
// that a project keyed by ProjectRoot shares one identity across all of its
// worktrees (the whole point of the function — see session.ProjectKey).
func TestProjectRootResolvesWorktreeToMainRepo(t *testing.T) {
	dir := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, dir, "worktree", "add", "-q", "-b", "tyci/child", wt)
	t.Cleanup(func() { run(t, dir, "worktree", "remove", "--force", wt) })

	mainRoot := ProjectRoot(dir)
	wtRoot := ProjectRoot(wt)
	if mainRoot == "" {
		t.Fatalf("ProjectRoot(main) = %q, want non-empty", mainRoot)
	}
	if wtRoot != mainRoot {
		t.Fatalf("ProjectRoot(worktree) = %q, want %q (same as main repo)", wtRoot, mainRoot)
	}
}

func TestProjectRootFromSubdirectory(t *testing.T) {
	dir := initRepo(t)
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	root := ProjectRoot(sub)
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if root != want {
		t.Fatalf("ProjectRoot(subdir) = %q, want %q", root, want)
	}
}

func TestProjectRootOutsideRepository(t *testing.T) {
	dir := t.TempDir() // not a git repo
	if got := ProjectRoot(dir); got != "" {
		t.Fatalf("ProjectRoot(non-repo) = %q, want \"\"", got)
	}
}

func TestBranchDetachedHead(t *testing.T) {
	fresh(t)
	dir := initRepo(t)
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := string(out[:40])
	run(t, dir, "checkout", "-q", "--detach")
	if got := Branch(dir); got != sha[:7] {
		t.Fatalf("Branch = %q, want %q", got, sha[:7])
	}
}

func TestBranchOutsideRepository(t *testing.T) {
	fresh(t)
	// t.TempDir is under /tmp (or /var), which is not a repository — but be
	// explicit rather than trusting the machine's layout.
	dir := t.TempDir()
	if got := Branch(dir); got != "" {
		t.Fatalf("Branch = %q, want empty outside a repo", got)
	}
	if got := Branch(""); got != "" {
		t.Fatalf("Branch(\"\") = %q, want empty", got)
	}
}

func TestBranchCachedThenExpires(t *testing.T) {
	fresh(t)
	dir := initRepo(t)
	if got := Branch(dir); got != "main" {
		t.Fatalf("Branch = %q, want main", got)
	}
	// Within the TTL the old answer stands even though HEAD moved: this is
	// the deliberate trade for not spawning work on every redraw.
	run(t, dir, "checkout", "-q", "-b", "later")
	if got := Branch(dir); got != "main" {
		t.Fatalf("cached Branch = %q, want stale main", got)
	}
	// Past the TTL it catches up.
	base := time.Now()
	now = func() time.Time { return base.Add(cacheTTL + time.Second) }
	t.Cleanup(func() { now = time.Now })
	if got := Branch(dir); got != "later" {
		t.Fatalf("Branch after TTL = %q, want later", got)
	}
}

func TestResolveRelativeGitdirPointer(t *testing.T) {
	fresh(t)
	// A pointer file with a relative gitdir, as git writes for submodules.
	root := t.TempDir()
	real := filepath.Join(root, "store")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "HEAD"), []byte("ref: refs/heads/sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".git"), []byte("gitdir: ../store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Branch(work); got != "sub" {
		t.Fatalf("Branch = %q, want sub", got)
	}
}
