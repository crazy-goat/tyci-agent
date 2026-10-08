package checks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// pushGh answers gh from env vars: PR_NUM (pr list answer, empty = none until created),
// GH_FAIL (fail the named subcommand: "list" or "create").
const pushGh = `
case "$*" in
  "pr list"*)
    [ "${GH_FAIL:-}" = list ] && exit 1
    if [ -n "${PR_NUM:-}" ] || grep -q "^pr create" "$GH_LOG"; then echo "${PR_NUM:-171}"; fi ;;
  "pr create"*) [ "${GH_FAIL:-}" = create ] && exit 1; echo "https://example/pull/171" ;;
  *) exit 2 ;;
esac
`

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type pushEnv struct {
	work, origin, runDir string
}

// newPushEnv makes a bare origin and a clone on branch issue-7 with one extra commit.
func newPushEnv(t *testing.T) pushEnv {
	t.Helper()
	root := t.TempDir()
	e := pushEnv{work: filepath.Join(root, "work"), origin: filepath.Join(root, "origin.git"), runDir: t.TempDir()}
	git(t, root, "init", "-q", "--bare", "-b", "main", e.origin)
	git(t, root, "clone", "-q", e.origin, e.work)
	git(t, e.work, "checkout", "-q", "-b", "main")
	git(t, e.work, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, e.work, "push", "-q", "origin", "main")
	git(t, e.work, "checkout", "-q", "-b", "issue-7")
	git(t, e.work, "commit", "-q", "--allow-empty", "-m", "feat: seven")
	return e
}

func (e pushEnv) run(t *testing.T, env map[string]string) (key string, exit int, stdout, stderr, log string) {
	t.Helper()
	logPath := testutil.StubGH(t, pushGh)
	base := map[string]string{"TYCI_BRANCH": "issue-7", "TYCI_DEFAULT_BRANCH": "main", "TYCI_RUN_DIR": e.runDir, "TYCI_REPO": "o/r", "TYCI_ISSUE": "7"}
	for k, v := range env {
		base[k] = v
	}
	script, _ := filepath.Abs("push.sh")
	key, exit, stdout, stderr = testutil.RunCheckIn(t, e.work, script, base)
	b, _ := os.ReadFile(logPath)
	return key, exit, stdout, stderr, string(b)
}

func (e pushEnv) remote(t *testing.T, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "-q", ref)
	cmd.Dir = e.origin
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func TestPush_PushesExplicitRef(t *testing.T) {
	e := newPushEnv(t)
	mainBefore := e.remote(t, "refs/heads/main")
	key, exit, _, _, _ := e.run(t, nil)
	if key != "ok" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if e.remote(t, "refs/heads/issue-7") != git(t, e.work, "rev-parse", "HEAD") {
		t.Fatal("issue-7 not pushed")
	}
	if e.remote(t, "refs/heads/main") != mainBefore {
		t.Fatal("main changed")
	}
}

func TestPush_PushesToPRBranch(t *testing.T) {
	e := newPushEnv(t)
	// open_pr.sh continued a PR that is open from another branch.
	if err := os.WriteFile(filepath.Join(e.runDir, "pr_branch"), []byte("feat/issue-7-slug\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	key, _, _, _, log := e.run(t, map[string]string{"PR_NUM": "171"})
	if key != "ok" || e.remote(t, "refs/heads/feat/issue-7-slug") != git(t, e.work, "rev-parse", "HEAD") {
		t.Fatalf("key=%q: the PR branch does not have the worktree HEAD", key)
	}
	if e.remote(t, "refs/heads/issue-7") != "" {
		t.Fatal("issue-7 was pushed although the PR is open from another branch")
	}
	if !strings.Contains(log, "--head feat/issue-7-slug") {
		t.Fatalf("the PR lookup does not use the PR branch: %s", log)
	}
}

func TestPush_RefusesDefaultBranch(t *testing.T) {
	e := newPushEnv(t)
	mainBefore := e.remote(t, "refs/heads/main")
	key, exit, _, stderr, _ := e.run(t, map[string]string{"TYCI_BRANCH": "main"})
	if key != "fail" || exit != 0 || e.remote(t, "refs/heads/main") != mainBefore {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	wantBlock(t, stderr, "fail", "refuses to push branch 'main'")
}

func TestPush_RefusesEmptyBranch(t *testing.T) {
	e := newPushEnv(t)
	key, exit, _, _, log := e.run(t, map[string]string{"TYCI_BRANCH": ""})
	if key != "fail" || exit != 0 || log != "" {
		t.Fatalf("key=%q exit=%d log=%q", key, exit, log)
	}
}

func TestPush_NeverForce(t *testing.T) {
	b, err := os.ReadFile("push.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"--force", "+refs/", "--force-with-lease", ":+"} {
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(line, bad) {
				t.Fatalf("script contains %q: %s", bad, line)
			}
		}
	}
	e := newPushEnv(t)
	// Diverge: origin gets a different commit on issue-7.
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", "-q", e.origin, other)
	git(t, other, "checkout", "-q", "-b", "issue-7")
	git(t, other, "commit", "-q", "--allow-empty", "-m", "remote work")
	git(t, other, "push", "-q", "origin", "issue-7")
	before := e.remote(t, "refs/heads/issue-7")
	key, exit, _, _, _ := e.run(t, nil)
	if key != "fail" || exit != 0 || e.remote(t, "refs/heads/issue-7") != before {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
}

func TestPush_WritesPRFile(t *testing.T) {
	e := newPushEnv(t)
	key, _, _, _, _ := e.run(t, map[string]string{"PR_NUM": "171"})
	got, _ := os.ReadFile(filepath.Join(e.runDir, "pr"))
	if key != "ok" || strings.TrimSpace(string(got)) != "171" {
		t.Fatalf("key=%q pr=%q", key, got)
	}
}

func TestPush_CreatesPRWhenNone(t *testing.T) {
	e := newPushEnv(t)
	_, _, _, _, log := e.run(t, nil)
	if strings.Count(log, "pr create") != 1 {
		t.Fatalf("log=%s", log)
	}
	got, _ := os.ReadFile(filepath.Join(e.runDir, "pr"))
	if strings.TrimSpace(string(got)) != "171" {
		t.Fatalf("pr=%q", got)
	}
	// Second run: the PR exists now (PR_NUM set), nothing is created.
	_, _, _, _, log = e.run(t, map[string]string{"PR_NUM": "171"})
	if strings.Contains(log, "pr create") {
		t.Fatalf("second run created a PR: %s", log)
	}
}

func TestPush_IgnoresClosedPR(t *testing.T) {
	e := newPushEnv(t)
	_, _, _, _, log := e.run(t, map[string]string{"PR_NUM": "171"})
	if !strings.Contains(log, "pr list") || !strings.Contains(log, "--state open") {
		t.Fatalf("log=%s", log)
	}
}

func TestPush_BodyClosesIssue(t *testing.T) {
	e := newPushEnv(t)
	_, _, _, _, log := e.run(t, nil)
	if !strings.Contains(log, "--body Closes #7") || !strings.Contains(log, "--title feat: seven") {
		t.Fatalf("log=%s", log)
	}
}

func TestPush_GhErrorExitsNonZero(t *testing.T) {
	for _, f := range []string{"list", "create"} {
		e := newPushEnv(t)
		key, exit, stdout, stderr, _ := e.run(t, map[string]string{"GH_FAIL": f})
		if exit == 0 || key != "" || stdout != "" {
			t.Fatalf("%s: key=%q exit=%d stdout=%q", f, key, exit, stdout)
		}
		wantBlock(t, stderr, "error", "failed with exit code")
	}
}
