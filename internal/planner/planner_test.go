package planner

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// gitRun runs git in dir and fails the test on error.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo builds a bare origin and a clone "work" with one commit on main,
// pushed, and origin/HEAD set. It also points HOME at a temp dir, so the
// worktrees never reach the real ~/.tyci.
func newRepo(t *testing.T) (work string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")

	tmp := t.TempDir()
	gitRun(t, tmp, "init", "--bare", "-q", "origin.git")
	work = filepath.Join(tmp, "work")
	gitRun(t, tmp, "clone", "-q", "origin.git", "work")
	gitRun(t, work, "checkout", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "x")
	gitRun(t, work, "commit", "-qm", "commit X")
	gitRun(t, work, "push", "-q", "-u", "origin", "main")
	gitRun(t, work, "remote", "set-head", "origin", "main")
	return work
}

func TestNew_CreatesWorktreeAtOnce(t *testing.T) {
	work := newRepo(t)

	s, err := New(work)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _, _ = s.Close() })

	if len(s.ID) != 16 {
		t.Fatalf("ID = %q, want 16 hex digits", s.ID)
	}
	if s.Branch != "plan-"+s.ID {
		t.Fatalf("Branch = %q, want plan-%s", s.Branch, s.ID)
	}
	home := os.Getenv("HOME")
	want := filepath.Join(home, ".tyci", "worktrees", "work", "plan-"+s.ID)
	if s.Worktree != want {
		t.Fatalf("Worktree = %q, want %q", s.Worktree, want)
	}
	if _, err := os.Stat(filepath.Join(s.Worktree, "x")); err != nil {
		t.Fatalf("worktree has no checkout of main: %v", err)
	}
	if got := gitRun(t, work, "rev-parse", "--verify", "--quiet", "refs/heads/"+s.Branch); got == "" {
		t.Fatalf("branch %s does not exist", s.Branch)
	}
}

func TestNew_TwoSessionsAreDistinct(t *testing.T) {
	work := newRepo(t)

	a, err := New(work)
	if err != nil {
		t.Fatalf("New a: %v", err)
	}
	t.Cleanup(func() { _, _ = a.Close() })
	b, err := New(work)
	if err != nil {
		t.Fatalf("New b: %v", err)
	}
	t.Cleanup(func() { _, _ = b.Close() })

	if a.ID == b.ID {
		t.Fatalf("both sessions have id %s", a.ID)
	}
	if a.Worktree == b.Worktree {
		t.Fatalf("both sessions share worktree %s", a.Worktree)
	}
	if a.Branch == b.Branch {
		t.Fatalf("both sessions share branch %s", a.Branch)
	}
	if a.Repo != b.Repo {
		t.Fatalf("Repo differs: %q and %q", a.Repo, b.Repo)
	}
}

func TestClose_CleanSessionIsRemoved(t *testing.T) {
	work := newRepo(t)
	s, err := New(work)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	kept, err := s.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if kept {
		t.Fatal("Close kept a clean session")
	}
	if _, err := os.Stat(s.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree %s still exists (err = %v)", s.Worktree, err)
	}
	if err := exec.Command("git", "-C", work, "rev-parse", "--verify", "--quiet", "refs/heads/"+s.Branch).Run(); err == nil {
		t.Fatalf("branch %s still exists after Close", s.Branch)
	}
}

func TestClose_CommitKeepsSession(t *testing.T) {
	work := newRepo(t)
	s, err := New(work)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _, _ = s.Close() })

	if err := os.WriteFile(filepath.Join(s.Worktree, "plan.md"), []byte("plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, s.Worktree, "add", "plan.md")
	gitRun(t, s.Worktree, "commit", "-qm", "plan")

	kept, err := s.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !kept {
		t.Fatal("Close removed a session with a commit")
	}
	if _, err := os.Stat(s.Worktree); err != nil {
		t.Fatalf("kept worktree is gone: %v", err)
	}
	if err := exec.Command("git", "-C", work, "rev-parse", "--verify", "--quiet", "refs/heads/"+s.Branch).Run(); err != nil {
		t.Fatalf("kept branch %s is gone", s.Branch)
	}
}

func TestClose_DirtyFileKeepsSession(t *testing.T) {
	work := newRepo(t)
	s, err := New(work)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _, _ = s.Close() })

	if err := os.WriteFile(filepath.Join(s.Worktree, "notes.md"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}

	kept, err := s.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !kept {
		t.Fatal("Close removed a session with an untracked file")
	}
	if _, err := os.Stat(filepath.Join(s.Worktree, "notes.md")); err != nil {
		t.Fatalf("untracked file is gone: %v", err)
	}
}

// TestPackageDoesNotImportOrchestratorOrFlow checks that a planner session
// never takes a worker slot. internal/flow imports internal/worktree, so
// worktree cannot import flow, and checking the direct imports of this
// package is enough.
func TestPackageDoesNotImportOrchestratorOrFlow(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(path, "/internal/orchestrator") || strings.HasSuffix(path, "/internal/flow") {
				t.Errorf("%s imports %s", name, path)
			}
		}
	}
}
