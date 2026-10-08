package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// openPRGh answers gh pr list with PR_JSON (the --json answer, [] when unset). GH_FAIL=list
// fails the call.
const openPRGh = `
case "$*" in
  "pr list"*) [ "${GH_FAIL:-}" = list ] && exit 1; echo "${PR_JSON:-[]}" ;;
  *) exit 2 ;;
esac
`

const (
	prOnRunBranch = `[{"number":171,"headRefName":"issue-7","isCrossRepository":false,"closingIssuesReferences":[{"number":7}]}]`
	prOtherBranch = `[{"number":171,"headRefName":"feat/issue-7-slug","isCrossRepository":false,"closingIssuesReferences":[{"number":7}]}]`
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
	testutil.StubGH(t, openPRGh)
	base := map[string]string{"TYCI_BRANCH": "issue-7", "TYCI_ISSUE": "7", "TYCI_DEFAULT_BRANCH": "main", "TYCI_RUN_DIR": e.runDir, "TYCI_REPO": "o/r"}
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
		key, exit, stderr := e.runOpenPR(t, map[string]string{"PR_JSON": prOnRunBranch})
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
	if b, _ := os.ReadFile(filepath.Join(e.runDir, "pr_branch")); strings.TrimSpace(string(b)) != "issue-7" {
		t.Fatalf("pr_branch file = %q", b)
	}
}

func TestOpenPR_FoundFromOtherBranch(t *testing.T) {
	e, issue := newOpenPREnv(t)
	// The PR of the earlier run is open from feat/issue-7-slug, which has a commit of its own,
	// so HEAD shows whether the script fetched the PR branch and not issue-7.
	git(t, e.work, "checkout", "-q", "-b", "feat", "origin/issue-7")
	writeCommit(t, e.work, "feat.txt", "f\n", "feat work")
	git(t, e.work, "push", "-q", "origin", "feat:refs/heads/feat/issue-7-slug")
	git(t, e.work, "checkout", "-q", "issue-7")
	feat := e.remote(t, "refs/heads/feat/issue-7-slug")
	if feat == issue {
		t.Fatal("the test needs the PR branch to differ from issue-7")
	}
	key, exit, stderr := e.runOpenPR(t, map[string]string{"PR_JSON": prOtherBranch})
	if key != "found" || exit != 0 {
		t.Fatalf("key=%q exit=%d stderr=%s", key, exit, stderr)
	}
	if got := git(t, e.work, "rev-parse", "HEAD"); got != feat {
		t.Fatalf("HEAD = %s, want the PR head %s", got, feat)
	}
	if b, _ := os.ReadFile(filepath.Join(e.runDir, "pr")); strings.TrimSpace(string(b)) != "171" {
		t.Fatalf("pr file = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(e.runDir, "pr_branch")); strings.TrimSpace(string(b)) != "feat/issue-7-slug" {
		t.Fatalf("pr_branch file = %q", b)
	}
}

func TestOpenPR_RunBranchPRWins(t *testing.T) {
	e, remote := newOpenPREnv(t)
	both := `[{"number":170,"headRefName":"feat/issue-7-slug","isCrossRepository":false,"closingIssuesReferences":[{"number":7}]},` +
		`{"number":171,"headRefName":"issue-7","isCrossRepository":false,"closingIssuesReferences":[{"number":7}]}]`
	key, exit, stderr := e.runOpenPR(t, map[string]string{"PR_JSON": both})
	if key != "found" || exit != 0 {
		t.Fatalf("key=%q exit=%d stderr=%s", key, exit, stderr)
	}
	if b, _ := os.ReadFile(filepath.Join(e.runDir, "pr")); strings.TrimSpace(string(b)) != "171" {
		t.Fatalf("pr file = %q, want the PR of the run branch", b)
	}
	if got := git(t, e.work, "rev-parse", "HEAD"); got != remote {
		t.Fatalf("HEAD = %s, want the PR head %s", got, remote)
	}
}

func TestOpenPR_MustCloseIssue(t *testing.T) {
	e, _ := newOpenPREnv(t)
	// Neither PR closes #7: one closes #70 and the other closes nothing.
	notClosing := `[{"number":170,"headRefName":"feat/a","isCrossRepository":false,"closingIssuesReferences":[]},` +
		`{"number":171,"headRefName":"feat/b","isCrossRepository":false,"closingIssuesReferences":[{"number":70}]}]`
	key, exit, stderr := e.runOpenPR(t, map[string]string{"PR_JSON": notClosing})
	if key != "none" || exit != 0 {
		t.Fatalf("key=%q exit=%d stderr=%s", key, exit, stderr)
	}
	if _, err := os.Stat(filepath.Join(e.runDir, "pr")); err == nil {
		t.Fatal("pr file written for a PR that does not close the issue")
	}
}

// A PR from a fork is never used: its head branch is not on origin. One fork PR closes #7
// from main, and the other has the run branch name.
func TestOpenPR_ForkPRIsNotUsed(t *testing.T) {
	e, _ := newOpenPREnv(t)
	head := git(t, e.work, "rev-parse", "HEAD")
	forks := `[{"number":172,"headRefName":"main","isCrossRepository":true,"closingIssuesReferences":[{"number":7}]},` +
		`{"number":173,"headRefName":"issue-7","isCrossRepository":true,"closingIssuesReferences":[]}]`
	key, exit, stderr := e.runOpenPR(t, map[string]string{"PR_JSON": forks})
	if key != "none" || exit != 0 {
		t.Fatalf("key=%q exit=%d stderr=%s", key, exit, stderr)
	}
	if git(t, e.work, "rev-parse", "HEAD") != head {
		t.Fatal("HEAD moved for a fork PR")
	}
	if _, err := os.Stat(filepath.Join(e.runDir, "pr")); err == nil {
		t.Fatal("pr file written for a fork PR")
	}
}

// The run branch PR is asked for by its head name first, so a repo with many open PRs still
// finds it.
func TestOpenPR_LooksUpRunBranchFirst(t *testing.T) {
	e, _ := newOpenPREnv(t)
	e.runOpenPR(t, nil)
	log, err := os.ReadFile(os.Getenv("GH_LOG"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "pr list -R o/r --head issue-7 --state open") {
		t.Fatalf("the first gh call does not look up the run branch:\n%s", log)
	}
}

func TestOpenPR_OwnWorkIsKept(t *testing.T) {
	e, _ := newOpenPREnv(t)
	writeCommit(t, e.work, "mine.txt", "m\n", "own work")
	head := git(t, e.work, "rev-parse", "HEAD")
	key, exit, stderr := e.runOpenPR(t, map[string]string{"PR_JSON": prOnRunBranch})
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
	key, exit, _ := e.runOpenPR(t, map[string]string{"PR_JSON": prOnRunBranch})
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
	key, exit, stderr := e.runOpenPR(t, map[string]string{"TYCI_BRANCH": "main", "PR_JSON": prOnRunBranch})
	if key != "fail" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	wantBlock(t, stderr, "fail", "default branch")
}
