package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo builds a repository with one commit, which is the minimum a worktree
// can branch from.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "seed")
	return dir
}

// TestIsolationGivesTheChildItsOwnCheckout is the whole point of the mode: the
// child's writes land somewhere the parent's tree cannot see, so two children
// writing at once cannot clobber each other.
func TestIsolationGivesTheChildItsOwnCheckout(t *testing.T) {
	repo := gitRepo(t)

	var childDir string
	runner := &mockRunner{RunTaskFunc: func(ctx context.Context, task, model string, opts SubagentOptions) (string, error) {
		childDir = Workdir(ctx)
		// Write through the same helper every tool uses, so this tests the
		// path a real child takes rather than the test's own idea of it.
		return "wrote it", os.WriteFile(resolvePath(ctx, "child.txt"), []byte("from the child\n"), 0o644)
	}}

	ctx := WithWorkdir(context.Background(), repo)
	res := runSingleTask(ctx, runner, subagentTask{Task: "write a file", Isolation: "worktree"}, 0, false)
	if !res.Success {
		t.Fatalf("task failed: %s", res.Error)
	}
	if childDir == "" || childDir == repo {
		t.Fatalf("child worked in %q, want a checkout of its own", childDir)
	}
	if _, err := os.Stat(filepath.Join(repo, "child.txt")); !os.IsNotExist(err) {
		t.Fatalf("the child's file appeared in the parent's tree; isolation buys nothing then")
	}
	if _, err := os.Stat(filepath.Join(childDir, "child.txt")); err != nil {
		t.Fatalf("the child's file is not in its own checkout either: %v", err)
	}
	// The parent has to be told where the work is, or it reports a change
	// nobody can find.
	for _, want := range []string{"isolation:", "branch tyci/", childDir, "git diff"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("result should say where the work went (%q missing): %q", want, res.Content)
		}
	}
}

// TestIsolationCleansUpWhenNothingChanged: most delegated work reads and
// reports. Keeping a branch and a directory for every one of those would bury
// the ones that matter.
func TestIsolationCleansUpWhenNothingChanged(t *testing.T) {
	repo := gitRepo(t)

	var childDir string
	runner := &mockRunner{RunTaskFunc: func(ctx context.Context, task, model string, opts SubagentOptions) (string, error) {
		childDir = Workdir(ctx)
		return "read it, changed nothing", nil
	}}

	ctx := WithWorkdir(context.Background(), repo)
	res := runSingleTask(ctx, runner, subagentTask{Task: "just read", Isolation: "worktree"}, 0, false)
	if !res.Success {
		t.Fatalf("task failed: %s", res.Error)
	}
	if _, err := os.Stat(childDir); !os.IsNotExist(err) {
		t.Errorf("checkout %s was left behind although nothing changed", childDir)
	}
	if !strings.Contains(res.Content, "changed no files") {
		t.Errorf("the result should say the checkout was removed: %q", res.Content)
	}
	out, err := exec.Command("git", "-C", repo, "branch", "--list", "tyci/*").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("branch left behind: %q", out)
	}
}

// TestIsolationOutsideARepoFailsTheTask: falling back to the shared directory
// would silently give the caller the opposite of what it asked for.
func TestIsolationOutsideARepoFailsTheTask(t *testing.T) {
	dir := t.TempDir()
	ran := false
	runner := &mockRunner{RunTaskFunc: func(ctx context.Context, task, model string, opts SubagentOptions) (string, error) {
		ran = true
		return "should never run", nil
	}}

	ctx := WithWorkdir(context.Background(), dir)
	res := runSingleTask(ctx, runner, subagentTask{Task: "write something", Isolation: "worktree"}, 0, false)
	if res.Success {
		t.Fatal("expected the task to fail rather than run unisolated")
	}
	if ran {
		t.Fatal("the child ran in the shared directory after isolation failed")
	}
	if !strings.Contains(res.Error, "isolation") {
		t.Errorf("error should name the cause: %q", res.Error)
	}
}

// TestUnknownIsolationModeIsRejected: a typo must not degrade to "no
// isolation", which looks identical to success.
func TestUnknownIsolationModeIsRejected(t *testing.T) {
	_, err := taskFromMap(map[string]any{"task": "t", "isolation": "container"})
	if err == nil {
		t.Fatal("expected an error for an unknown mode")
	}
	if !strings.Contains(err.Error(), "worktree") {
		t.Errorf("the error should name the mode that does exist: %v", err)
	}

	for _, v := range []string{"", "none"} {
		task, err := taskFromMap(map[string]any{"task": "t", "isolation": v})
		if err != nil {
			t.Fatalf("isolation=%q should be accepted as 'share the parent's directory': %v", v, err)
		}
		if task.Isolation != "" {
			t.Errorf("isolation=%q became %q", v, task.Isolation)
		}
	}
}
