package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

func writeCommit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", file)
	git(t, dir, "commit", "-q", "-m", msg)
}

// advanceMain pushes a new commit to origin main from a second clone.
func advanceMain(t *testing.T, e pushEnv, file, content string) {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", "-q", e.origin, other)
	writeCommit(t, other, file, content, "main moves")
	git(t, other, "push", "-q", "origin", "main")
}

func (e pushEnv) runRebase(t *testing.T, script string, env map[string]string) (key string, exit int, stdout, stderr, log string) {
	t.Helper()
	logPath := testutil.StubGH(t, pushGh)
	base := map[string]string{"TYCI_BRANCH": "issue-7", "TYCI_DEFAULT_BRANCH": "main", "TYCI_RUN_DIR": e.runDir, "TYCI_REPO": "o/r", "TYCI_ISSUE": "7", "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}
	for k, v := range env {
		base[k] = v
	}
	abs, _ := filepath.Abs(script)
	key, exit, stdout, stderr = testutil.RunCheckIn(t, e.work, abs, base)
	b, _ := os.ReadFile(logPath)
	return key, exit, stdout, stderr, string(b)
}

func TestRebase_CleanMergePushes(t *testing.T) {
	e := newPushEnv(t)
	advanceMain(t, e, "other.txt", "x\n")
	key, exit, _, _, log := e.runRebase(t, "rebase.sh", map[string]string{"PR_NUM": "171"})
	if key != "ok" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if e.remote(t, "refs/heads/issue-7") != git(t, e.work, "rev-parse", "HEAD") {
		t.Fatal("issue-7 not pushed")
	}
	if _, err := os.Stat(filepath.Join(e.work, "other.txt")); err != nil {
		t.Fatal("main commit not merged")
	}
	if strings.Contains(log, "pr merge") {
		t.Fatalf("log=%s", log)
	}
}

func TestRebase_AlreadyUpToDateOk(t *testing.T) {
	e := newPushEnv(t)
	key, exit, _, _, _ := e.runRebase(t, "rebase.sh", nil)
	if key != "ok" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
}

func TestRebase_ConflictAborts(t *testing.T) {
	e := newPushEnv(t)
	writeCommit(t, e.work, "f.txt", "mine\n", "issue change")
	advanceMain(t, e, "f.txt", "theirs\n")
	head := git(t, e.work, "rev-parse", "HEAD")
	key, exit, _, _, _ := e.runRebase(t, "rebase.sh", nil)
	if key != "conflict" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if s := git(t, e.work, "status", "--porcelain"); s != "" {
		t.Fatalf("dirty: %s", s)
	}
	if _, err := os.Stat(filepath.Join(e.work, ".git", "MERGE_HEAD")); err == nil {
		t.Fatal("merge in progress")
	}
	if git(t, e.work, "rev-parse", "HEAD") != head {
		t.Fatal("HEAD moved")
	}
}

func TestRebase_FetchFailureFails(t *testing.T) {
	e := newPushEnv(t)
	git(t, e.work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	key, exit, _, _, _ := e.runRebase(t, "rebase.sh", nil)
	if key != "fail" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
}

func TestRebase_PushRejectedFails(t *testing.T) {
	e := newPushEnv(t)
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", "-q", e.origin, other)
	git(t, other, "checkout", "-q", "-b", "issue-7")
	git(t, other, "commit", "-q", "--allow-empty", "-m", "remote work")
	git(t, other, "push", "-q", "origin", "issue-7")
	advanceMain(t, e, "other.txt", "x\n")
	key, exit, _, _, _ := e.runRebase(t, "rebase.sh", nil)
	if key != "fail" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
}

func TestRebase_MissingPushShFails(t *testing.T) {
	e := newPushEnv(t)
	src, err := os.ReadFile("rebase.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rebase.sh"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	key, exit, _, stderr, _ := e.runRebase(t, filepath.Join(dir, "rebase.sh"), nil)
	if key != "fail" || exit != 0 || !strings.Contains(stderr, "push.sh not found") {
		t.Fatalf("key=%q exit=%d stderr=%q", key, exit, stderr)
	}
}

func TestRebase_UsesExplicitRefOnPush(t *testing.T) {
	e := newPushEnv(t)
	advanceMain(t, e, "other.txt", "x\n")
	mainBefore := e.remote(t, "refs/heads/main")
	key, _, _, _, _ := e.runRebase(t, "rebase.sh", nil)
	if key != "ok" || e.remote(t, "refs/heads/main") != mainBefore {
		t.Fatalf("key=%q main changed", key)
	}
	if e.remote(t, "refs/heads/issue-7") == "" {
		t.Fatal("issue-7 missing")
	}
}

func TestRebase_ChangelogOnlyConflictKeepsBoth(t *testing.T) {
	e := newPushEnv(t)
	writeCommit(t, e.work, "CHANGELOG.md", "# Changelog\n\nold\n", "base")
	git(t, e.work, "push", "-q", "origin", "issue-7:main")
	writeCommit(t, e.work, "CHANGELOG.md", "# Changelog\nmine\n\nold\n", "issue change")
	advanceMain(t, e, "CHANGELOG.md", "# Changelog\ntheirs\n\nold\n")
	key, exit, _, _, _ := e.runRebase(t, "rebase.sh", map[string]string{"PR_NUM": "171"})
	if key != "ok" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	b, _ := os.ReadFile(filepath.Join(e.work, "CHANGELOG.md"))
	if !strings.Contains(string(b), "mine") || !strings.Contains(string(b), "theirs") || strings.Contains(string(b), "<<<<") {
		t.Fatalf("changelog = %q", b)
	}
}

func TestRebase_ChangelogAndCodeConflictStaysConflict(t *testing.T) {
	e := newPushEnv(t)
	writeCommit(t, e.work, "CHANGELOG.md", "old\n", "base")
	writeCommit(t, e.work, "f.txt", "old\n", "base f")
	git(t, e.work, "push", "-q", "origin", "issue-7:main")
	writeCommit(t, e.work, "CHANGELOG.md", "mine\n", "issue change")
	writeCommit(t, e.work, "f.txt", "mine\n", "code")
	advanceMain(t, e, "CHANGELOG.md", "theirs\n")
	other := filepath.Join(t.TempDir(), "o2")
	git(t, filepath.Dir(other), "clone", "-q", e.origin, other)
	writeCommit(t, other, "f.txt", "theirs\n", "code too")
	git(t, other, "push", "-q", "origin", "main")
	key, _, _, _, _ := e.runRebase(t, "rebase.sh", nil)
	if key != "conflict" {
		t.Fatalf("key=%q", key)
	}
	if s := git(t, e.work, "status", "--porcelain"); s != "" {
		t.Fatalf("dirty: %s", s)
	}
}
