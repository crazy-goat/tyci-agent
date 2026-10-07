package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// newOpenPREnv is a fresh run worktree: local issue-7 at origin/main, and an issue-7 on
// origin with one commit of an earlier run. It returns the remote issue-7 head.
func newOpenPREnv(t *testing.T) (pushEnv, string) {
	t.Helper()
	e := newPushEnv(t)
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", "-q", e.origin, other)
	git(t, other, "checkout", "-q", "-b", "issue-7")
	writeCommit(t, other, "earlier.txt", "x\n", "earlier run")
	git(t, other, "push", "-q", "origin", "issue-7")
	git(t, e.work, "fetch", "-q", "origin")
	git(t, e.work, "reset", "-q", "--hard", "origin/main")
	return e, e.remote(t, "refs/heads/issue-7")
}

func (e pushEnv) runOpenPR(t *testing.T, env map[string]string) (key string, exit int, stderr string) {
	t.Helper()
	testutil.StubGH(t, pushGh)
	base := map[string]string{"TYCI_BRANCH": "issue-7", "TYCI_DEFAULT_BRANCH": "main", "TYCI_RUN_DIR": e.runDir, "TYCI_REPO": "o/r"}
	for k, v := range env {
		base[k] = v
	}
	script, _ := filepath.Abs("open_pr.sh")
	key, exit, _, stderr = testutil.RunCheckIn(t, e.work, script, base)
	return key, exit, stderr
}

func TestOpenPR_NoPRCodesFromMain(t *testing.T) {
	e, _ := newOpenPREnv(t)
	head := git(t, e.work, "rev-parse", "HEAD")
	key, exit, stderr := e.runOpenPR(t, nil)
	if key != "none" || exit != 0 {
		t.Fatalf("key=%q exit=%d stderr=%s", key, exit, stderr)
	}
	if git(t, e.work, "rev-parse", "HEAD") != head {
		t.Fatal("HEAD moved without an open PR")
	}
	if _, err := os.Stat(filepath.Join(e.runDir, "pr")); err == nil {
		t.Fatal("pr file written without an open PR")
	}
}

func TestOpenPR_FoundMovesToPRHead(t *testing.T) {
	e, remote := newOpenPREnv(t)
	for i := 0; i < 2; i++ { // the second run is idempotent
		key, exit, stderr := e.runOpenPR(t, map[string]string{"PR_NUM": "171"})
		if key != "found" || exit != 0 {
			t.Fatalf("run %d: key=%q exit=%d stderr=%s", i, key, exit, stderr)
		}
		if !strings.Contains(stderr, "continuing PR #171") {
			t.Errorf("stderr does not say what happens: %s", stderr)
		}
	}
	if got := git(t, e.work, "rev-parse", "HEAD"); got != remote {
		t.Fatalf("HEAD = %s, want the PR head %s", got, remote)
	}
	if b, _ := os.ReadFile(filepath.Join(e.runDir, "pr")); strings.TrimSpace(string(b)) != "171" {
		t.Fatalf("pr file = %q", b)
	}
}

func TestOpenPR_OwnWorkIsKept(t *testing.T) {
	e, _ := newOpenPREnv(t)
	writeCommit(t, e.work, "mine.txt", "m\n", "own work")
	head := git(t, e.work, "rev-parse", "HEAD")
	key, exit, stderr := e.runOpenPR(t, map[string]string{"PR_NUM": "171"})
	if key != "fail" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if git(t, e.work, "rev-parse", "HEAD") != head {
		t.Fatal("own commit was dropped")
	}
	wantBlock(t, stderr, "fail", "git reset --hard origin/issue-7")
}

func TestOpenPR_DirtyWorktreeFails(t *testing.T) {
	e, _ := newOpenPREnv(t)
	if err := os.WriteFile(filepath.Join(e.work, "x.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	key, exit, _ := e.runOpenPR(t, map[string]string{"PR_NUM": "171"})
	if key != "fail" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
}

func TestOpenPR_GhFailureHasNoKey(t *testing.T) {
	e, _ := newOpenPREnv(t)
	key, exit, stderr := e.runOpenPR(t, map[string]string{"GH_FAIL": "list"})
	if key != "" || exit == 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	wantBlock(t, stderr, "error", "gh pr list")
}

func TestOpenPR_DefaultBranchRefused(t *testing.T) {
	e, _ := newOpenPREnv(t)
	key, exit, stderr := e.runOpenPR(t, map[string]string{"TYCI_BRANCH": "main", "PR_NUM": "171"})
	if key != "fail" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	wantBlock(t, stderr, "fail", "default branch")
}
