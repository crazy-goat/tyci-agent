package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitRun(t *testing.T, dir string, args ...string) {
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

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", name)
	gitRun(t, dir, "commit", "-qm", msg)
}

// newRepoWithOrigin builds a bare origin with one commit X on main and a
// clone ("work") with X pushed. Callers add unpushed commits or push new
// ones from another clone to diverge from origin.
func newRepoWithOrigin(t *testing.T) (work, origin string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")

	tmp := t.TempDir()
	origin = filepath.Join(tmp, "origin.git")
	gitRun(t, tmp, "init", "--bare", "-q", "origin.git")
	work = filepath.Join(tmp, "work")
	gitRun(t, tmp, "clone", "-q", "origin.git", "work")
	gitRun(t, work, "checkout", "-q", "-b", "main")
	commitFile(t, work, "x", "x", "commit X")
	gitRun(t, work, "push", "-q", "-u", "origin", "main")
	gitRun(t, tmp, "--git-dir=origin.git", "symbolic-ref", "HEAD", "refs/heads/main")
	return work, origin
}

func TestAddIssue_BranchesFromOrigin(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	pushed := gitOut(t, work, "rev-parse", "HEAD")

	// Local HEAD moves ahead with an unpushed commit Y.
	commitFile(t, work, "y", "y", "commit Y (unpushed)")
	local := gitOut(t, work, "rev-parse", "HEAD")
	if local == pushed {
		t.Fatal("test setup: unpushed commit did not move HEAD")
	}

	wt, err := AddIssue(context.Background(), t.TempDir(), work, 1, "main")
	if err != nil {
		t.Fatalf("AddIssue: %v", err)
	}
	t.Cleanup(func() { _ = wt.Remove(context.Background()) })

	if wt.Branch != "issue-1" {
		t.Fatalf("Branch = %q, want issue-1", wt.Branch)
	}
	head := gitOut(t, wt.Dir, "rev-parse", "HEAD")
	if head != pushed {
		t.Fatalf("worktree HEAD = %s, want pushed commit %s (local HEAD is %s)", head, pushed, local)
	}
	if wt.BaseCommit != pushed {
		t.Fatalf("BaseCommit = %s, want %s", wt.BaseCommit, pushed)
	}
}

func TestAddIssue_LocationUnderHome(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	home := t.TempDir()

	wt, err := AddIssue(context.Background(), home, work, 7, "main")
	if err != nil {
		t.Fatalf("AddIssue: %v", err)
	}
	t.Cleanup(func() { _ = wt.Remove(context.Background()) })

	root, err := Root(context.Background(), work)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	want := filepath.Join(home, ".tyci", "worktrees", filepath.Base(root), "issue-7")
	if wt.Dir != want {
		t.Fatalf("Dir = %q, want %q", wt.Dir, want)
	}
	if wt.Branch != "issue-7" {
		t.Fatalf("Branch = %q, want issue-7", wt.Branch)
	}
}

func TestAddIssue_ExistingDirFails(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	home := t.TempDir()
	ctx := context.Background()

	wt, err := AddIssue(ctx, home, work, 3, "main")
	if err != nil {
		t.Fatalf("AddIssue: %v", err)
	}
	t.Cleanup(func() { _ = wt.Remove(context.Background()) })

	_, err = AddIssue(ctx, home, work, 3, "main")
	if err == nil {
		t.Fatal("second AddIssue for the same issue succeeded, want an error")
	}
	if !strings.Contains(err.Error(), wt.Dir) {
		t.Fatalf("error %q does not mention the worktree path %q", err, wt.Dir)
	}
}

func TestAddIssue_ExistingBranchFails(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	gitRun(t, work, "branch", "issue-5")

	_, err := AddIssue(context.Background(), t.TempDir(), work, 5, "main")
	if err == nil {
		t.Fatal("AddIssue with an existing branch succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "issue-5") {
		t.Fatalf("error %q does not mention the branch issue-5", err)
	}
}

func TestAddIssue_FetchesLatestOrigin(t *testing.T) {
	work, origin := newRepoWithOrigin(t)

	// Push a new commit from another clone, so work's origin/main is stale.
	parent := t.TempDir()
	other := filepath.Join(parent, "other")
	gitRun(t, parent, "clone", "-q", origin, "other")
	gitRun(t, other, "checkout", "-q", "main")
	commitFile(t, other, "z", "z", "commit Z from another clone")
	gitRun(t, other, "push", "-q", "origin", "main")
	latest := gitOut(t, other, "rev-parse", "HEAD")

	wt, err := AddIssue(context.Background(), t.TempDir(), work, 2, "main")
	if err != nil {
		t.Fatalf("AddIssue: %v", err)
	}
	t.Cleanup(func() { _ = wt.Remove(context.Background()) })

	head := gitOut(t, wt.Dir, "rev-parse", "HEAD")
	if head != latest {
		t.Fatalf("worktree HEAD = %s, want latest origin commit %s", head, latest)
	}
}

func TestAddIssue_RejectsBadArgs(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	home := t.TempDir()
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		home   string
		issue  int
		branch string
	}{
		{"zero issue", home, 0, "main"},
		{"negative issue", home, -1, "main"},
		{"empty default branch", home, 1, ""},
		{"empty home", "", 1, "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := AddIssue(ctx, tc.home, work, tc.issue, tc.branch); err == nil {
				t.Fatalf("AddIssue(%q, %d, %q) succeeded, want an error", tc.home, tc.issue, tc.branch)
			}
		})
	}
}

func TestAddIssue_FetchFailureIsError(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	gitRun(t, work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "nonexistent.git"))

	_, err := AddIssue(context.Background(), t.TempDir(), work, 4, "main")
	if err == nil {
		t.Fatal("AddIssue with an unreachable origin succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("error %q does not mention the fetch failure", err)
	}
}

// commitSetup commits bin/worktree-setup.sh with the given content and mode to
// main and pushes it, so that AddIssue finds it in origin/main.
func commitSetup(t *testing.T, work, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(work, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, "bin", "worktree-setup.sh")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "bin/worktree-setup.sh")
	gitRun(t, work, "commit", "-qm", "add setup script")
	gitRun(t, work, "push", "-q", "origin", "main")
}

func TestAddIssue_RunsSetupScriptOnceInWorktree(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	runs := filepath.Join(t.TempDir(), "runs")
	commitSetup(t, work, "#!/bin/sh\npwd -P >> '"+runs+"'\n", 0o755)

	wt, err := AddIssue(context.Background(), t.TempDir(), work, 8, "main")
	if err != nil {
		t.Fatalf("AddIssue: %v", err)
	}
	t.Cleanup(func() { _ = wt.Remove(context.Background()) })

	got, err := os.ReadFile(runs)
	if err != nil {
		t.Fatalf("setup script did not run: %v", err)
	}
	want, err := filepath.EvalSymlinks(wt.Dir)
	if err != nil {
		t.Fatal(err)
	}
	// One line means one run; a second run would add a second line.
	if strings.TrimSpace(string(got)) != want {
		t.Fatalf("setup script output = %q, want one run in %s", got, want)
	}
}

func TestAddIssue_SkipsNonExecutableSetupScript(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	runs := filepath.Join(t.TempDir(), "runs")
	commitSetup(t, work, "#!/bin/sh\necho ran >> '"+runs+"'\n", 0o644)

	wt, err := AddIssue(context.Background(), t.TempDir(), work, 13, "main")
	if err != nil {
		t.Fatalf("AddIssue: %v", err)
	}
	t.Cleanup(func() { _ = wt.Remove(context.Background()) })

	if _, err := os.Stat(runs); !os.IsNotExist(err) {
		t.Fatalf("non-executable setup script ran (err = %v)", err)
	}
}

func TestAddIssue_FailingSetupScriptFailsAndLeavesNothing(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	commitSetup(t, work, "#!/bin/sh\necho 'setup broke here' >&2\nexit 1\n", 0o755)
	home := t.TempDir()

	_, err := AddIssue(context.Background(), home, work, 10, "main")
	if err == nil {
		t.Fatal("AddIssue with a failing setup script succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "setup broke here") {
		t.Fatalf("error %q does not carry the script output", err)
	}

	target := filepath.Join(home, ".tyci", "worktrees", filepath.Base(work), "issue-10")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("worktree %s is left behind after a failed setup (err = %v)", target, err)
	}
	if out, err := exec.Command("git", "-C", work, "rev-parse", "--verify", "--quiet", "refs/heads/issue-10").CombinedOutput(); err == nil {
		t.Fatalf("branch issue-10 is left behind after a failed setup: %s", out)
	}
}

// TestAddIssue_CancelledDuringSetupLeavesNothing cancels the run while the
// setup script is running. The script runs a child (sleep) that holds the
// output pipe open. AddIssue must return soon after the cancel, and must
// leave neither the worktree nor the branch behind.
func TestAddIssue_CancelledDuringSetupLeavesNothing(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	started := filepath.Join(t.TempDir(), "started")
	commitSetup(t, work, "#!/bin/sh\ntouch '"+started+"'\nsleep 30\n", 0o755)
	home := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel only when the script has started, so the cancel always hits the setup.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			if _, err := os.Stat(started); err == nil {
				cancel()
				return
			}
		}
	}()

	begin := time.Now()
	_, err := AddIssue(ctx, home, work, 15, "main")
	elapsed := time.Since(begin)

	if _, statErr := os.Stat(started); statErr != nil {
		t.Fatalf("setup script did not start, so the cancel did not reach it (err = %v, AddIssue err = %v)", statErr, err)
	}
	if err == nil {
		t.Fatal("AddIssue with a cancelled setup succeeded, want an error")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("AddIssue returned after %v; the script runs 30s, so the cancel did not stop it", elapsed)
	}

	target := filepath.Join(home, ".tyci", "worktrees", filepath.Base(work), "issue-15")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("worktree %s is left behind after a cancelled setup (err = %v)", target, err)
	}
	if out, err := exec.Command("git", "-C", work, "rev-parse", "--verify", "--quiet", "refs/heads/issue-15").CombinedOutput(); err == nil {
		t.Fatalf("branch issue-15 is left behind after a cancelled setup: %s", out)
	}
}

func TestRemove_IssueWorktreeKeepsParent(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	home := t.TempDir()
	ctx := context.Background()

	wt11, err := AddIssue(ctx, home, work, 11, "main")
	if err != nil {
		t.Fatalf("AddIssue 11: %v", err)
	}
	wt12, err := AddIssue(ctx, home, work, 12, "main")
	if err != nil {
		t.Fatalf("AddIssue 12: %v", err)
	}
	t.Cleanup(func() { _ = wt12.Remove(context.Background()) })

	if err := wt11.Remove(ctx); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(wt11.Dir); !os.IsNotExist(err) {
		t.Fatalf("removed worktree %s still exists after Remove (err = %v)", wt11.Dir, err)
	}
	if _, err := os.Stat(wt12.Dir); err != nil {
		t.Fatalf("sibling worktree %s is gone after removing issue-11: %v", wt12.Dir, err)
	}
	parent := filepath.Dir(wt11.Dir)
	if _, err := os.Stat(parent); err != nil {
		t.Fatalf("shared parent %s is gone after removing issue-11: %v", parent, err)
	}
	if out, err := exec.Command("git", "-C", work, "rev-parse", "--verify", "--quiet", "refs/heads/issue-11").CombinedOutput(); err == nil {
		t.Fatalf("branch issue-11 still exists after Remove: %s", out)
	}
}

func TestAdd_StillUsesTempDir(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	ctx := context.Background()

	w, err := Add(ctx, work, "sometask")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	parent := filepath.Dir(w.Dir)
	if err := w.Remove(ctx); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("temp parent %s still exists after Remove (err = %v)", parent, err)
	}
}
