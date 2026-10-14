package checks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// stubGo is a fake go first on PATH. On 'go get' it appends GO_BUMP to go.sum and GO_MOD_LINE to
// go.mod when they are set, and it fails when GO_FAIL is set. It never reaches the network.
const stubGo = `
if [ -n "${GO_FAIL:-}" ]; then echo "go: boom" >&2; exit 1; fi
if [ "$1 $2" = "get -u" ]; then
  if [ -n "${GO_BUMP:-}" ]; then printf '%s\n' "$GO_BUMP" >>go.sum; fi
  if [ -n "${GO_MOD_LINE:-}" ]; then printf '%s\n' "$GO_MOD_LINE" >>go.mod; fi
fi
exit 0
`

func stubGoCmd(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/usr/bin/env bash\n"+stubGo), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// bumpRepo makes a git repo with a committed go.mod and go.sum, and returns its dir.
func bumpRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), []byte("old v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "init", "-q")
	gitOut(t, dir, "add", "go.mod", "go.sum")
	gitOut(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "init")
	return dir
}

// runBump runs bump_deps.sh in repo with the fake go and returns the key, the run dir and the exit code.
func runBump(t *testing.T, repo string, env map[string]string) (key, runDir string, exit int) {
	t.Helper()
	stubGoCmd(t)
	runDir = t.TempDir()
	script, _ := filepath.Abs("bump_deps.sh")
	base := map[string]string{
		"TYCI_RUN_DIR":        runDir,
		"GIT_AUTHOR_NAME":     "tyci",
		"GIT_AUTHOR_EMAIL":    "tyci@example.com",
		"GIT_COMMITTER_NAME":  "tyci",
		"GIT_COMMITTER_EMAIL": "tyci@example.com",
	}
	for k, v := range env {
		base[k] = v
	}
	key, exit, _, _ = testutil.RunCheckIn(t, repo, script, base)
	return key, runDir, exit
}

// gitOut runs git in dir and returns its trimmed output.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestBumpDepsNoChange(t *testing.T) {
	repo := bumpRepo(t)
	key, runDir, exit := runBump(t, repo, nil)
	if exit != 0 || key != "nochange" {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if _, err := os.Stat(filepath.Join(runDir, "ask-table.md")); err == nil {
		t.Fatal("ask-table.md written by bump_deps")
	}
	if n := gitOut(t, repo, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("commits = %s, want 1", n)
	}
}

func TestBumpDepsChangedCommits(t *testing.T) {
	repo := bumpRepo(t)
	token := "ghp_" + strings.Repeat("a", 36)
	key, runDir, exit := runBump(t, repo, map[string]string{
		"GO_BUMP":     "new v2.0.0 " + token,
		"GO_MOD_LINE": "require example.com/new v2.0.0",
	})
	if exit != 0 || key != "changed" {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if subj := gitOut(t, repo, "log", "-1", "--format=%s"); subj != "chore(deps): bump Go dependencies" {
		t.Fatalf("subject = %q", subj)
	}
	if _, err := os.Stat(filepath.Join(runDir, "ask-table.md")); err == nil {
		t.Fatal("bump_deps wrote ask-table.md; the ask_table state does that")
	}
}

func TestBumpDepsGoFails(t *testing.T) {
	repo := bumpRepo(t)
	key, runDir, exit := runBump(t, repo, map[string]string{"GO_FAIL": "1"})
	if exit != 0 || key != "fail" {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if _, err := os.Stat(filepath.Join(runDir, "ask-table.md")); err == nil {
		t.Fatal("ask-table.md written after go failed")
	}
}
