package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// wantBlock requires the describe.sh block (#369) in stderr: all six fields once, the
// result, and want somewhere in the block.
func wantBlock(t *testing.T, stderr, result, want string) {
	t.Helper()
	for _, f := range []string{"RESULT: " + result + "\n", "STEP: ", "WHAT: ", "STATE: ", "LIKELY CAUSE: ", "SUGGESTED: "} {
		if n := strings.Count(stderr, "\n"+f); n != 1 {
			t.Errorf("stderr has %d lines %q, want 1:\n%s", n, f, stderr)
		}
	}
	if block := stderr[strings.LastIndex(stderr, "\nRESULT: ")+1:]; !strings.Contains(block, want) {
		t.Errorf("block does not contain %q:\n%s", want, block)
	}
}

// libScript writes body after the sourcing lines of the builtin scripts, next to a copy of describe.sh.
func libScript(t *testing.T, body string) string {
	t.Helper()
	lib, err := os.ReadFile("describe.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "describe.sh"), lib, 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "x.sh")
	head := "set -euo pipefail\n. \"$(dirname \"${BASH_SOURCE[0]}\")/describe.sh\"\n"
	if err := os.WriteFile(script, []byte(head+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestDescribe_StateFromGit(t *testing.T) {
	e := newPushEnv(t)
	script := libScript(t, `describe fail "it broke" "a cause" "do this" "extra fact"; echo fail`+"\n")
	key, _, _, stderr := testutil.RunCheckIn(t, e.work, script, map[string]string{
		"TYCI_STATE": "update", "TYCI_BRANCH": "issue-7", "TYCI_DEFAULT_BRANCH": "main", "TYCI_PR": "42",
	})
	if key != "fail" {
		t.Fatalf("key=%q", key)
	}
	wantBlock(t, stderr, "fail", "WHAT: it broke\n")
	head := git(t, e.work, "rev-parse", "--short", "HEAD")
	for _, s := range []string{"STEP: update\n", "STATE: local issue-7 = " + head, "origin/main = ", "origin/issue-7 = missing", "worktree clean", "PR #42; extra fact\n", "LIKELY CAUSE: a cause\n", "SUGGESTED: do this\n"} {
		if !strings.Contains(stderr, s) {
			t.Errorf("stderr lacks %q:\n%s", s, stderr)
		}
	}
}

// After open_pr.sh continued a PR from another branch, STATE shows the PR head branch on origin,
// not the run branch name (#374).
func TestDescribe_StateShowsPRBranch(t *testing.T) {
	e := newPushEnv(t)
	git(t, e.work, "push", "-q", "origin", "HEAD:refs/heads/feat/issue-7-slug")
	if err := os.WriteFile(filepath.Join(e.runDir, "pr_branch"), []byte("feat/issue-7-slug\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := libScript(t, `describe fail "it broke" "a cause" "do this"; echo fail`+"\n")
	_, _, _, stderr := testutil.RunCheckIn(t, e.work, script, map[string]string{
		"TYCI_STATE": "update", "TYCI_BRANCH": "issue-7", "TYCI_DEFAULT_BRANCH": "main", "TYCI_RUN_DIR": e.runDir,
	})
	wantBlock(t, stderr, "fail", "WHAT: it broke\n")
	head := git(t, e.work, "rev-parse", "--short", "HEAD")
	if !strings.Contains(stderr, "STATE: local issue-7 = "+head+"; origin/main = ") {
		t.Errorf("stderr lacks the local run branch:\n%s", stderr)
	}
	if !strings.Contains(stderr, "origin/feat/issue-7-slug = "+head) {
		t.Errorf("stderr lacks the PR head branch on origin:\n%s", stderr)
	}
	if strings.Contains(stderr, "origin/issue-7") {
		t.Errorf("stderr names the run branch on origin:\n%s", stderr)
	}
}

// A failed command stops the script without a key; the ERR trap prints one block, also when the
// command ran in a command substitution (a subshell).
func TestDescribe_ErrTrapPrintsOneBlock(t *testing.T) {
	script := libScript(t, "x=$(false)\necho never\n")
	key, exit, _, stderr := testutil.RunCheckIn(t, t.TempDir(), script, nil)
	if key != "" || exit == 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	wantBlock(t, stderr, "error", "the command 'x=$(false)' failed")
}

func TestDescribe_QuietOnSuccess(t *testing.T) {
	script := libScript(t, "if ! false; then :; fi\nx=$(false || true)\necho ok\n")
	key, _, _, stderr := testutil.RunCheckIn(t, t.TempDir(), script, nil)
	if key != "ok" || stderr != "" {
		t.Fatalf("key=%q stderr=%q", key, stderr)
	}
}

// The lock script has no failure key; an error (here: HOME is a file) prints the block.
func TestLock_ErrorPrintsBlock(t *testing.T) {
	home := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(home, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs("lock.sh")
	key, exit, stderr := testutil.RunCheck(t, abs, map[string]string{"HOME": home, "TYCI_REPO": "o/r", "TYCI_RUN_DIR": t.TempDir()})
	if key != "" || exit == 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	wantBlock(t, stderr, "error", "mkdir")
}
