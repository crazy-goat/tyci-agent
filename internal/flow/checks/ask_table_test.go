package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// runAskTable runs ask_table.sh in repo with TYCI_DEFAULT_BRANCH=main and returns the key, the run dir and the exit code.
func runAskTable(t *testing.T, repo string) (key, runDir string, exit int) {
	t.Helper()
	runDir = t.TempDir()
	script, _ := filepath.Abs("ask_table.sh")
	env := map[string]string{"TYCI_RUN_DIR": runDir, "TYCI_DEFAULT_BRANCH": "main"}
	key, exit, _, _ = testutil.RunCheckIn(t, repo, script, env)
	return key, runDir, exit
}

// askRepo makes a repo whose origin/main is the bumpRepo commit, then commits a bump on a branch.
func askRepo(t *testing.T) string {
	t.Helper()
	repo := bumpRepo(t)
	gitOut(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	return repo
}

func commitGoMod(t *testing.T, repo, line, msg string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(repo, "go.mod"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	gitOut(t, repo, "add", "go.mod")
	gitOut(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", msg)
}

func TestAskTableShowsBranchDiff(t *testing.T) {
	repo := askRepo(t)
	token := "ghp_" + strings.Repeat("a", 36)
	commitGoMod(t, repo, "require example.com/new v2.0.0 // "+token, "chore(deps): bump Go dependencies")
	key, runDir, exit := runAskTable(t, repo)
	if exit != 0 || key != "ok" {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	table, err := os.ReadFile(filepath.Join(runDir, "ask-table.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"```diff", "+require example.com/new v2.0.0"} {
		if !strings.Contains(string(table), want) {
			t.Fatalf("ask-table.md lacks %q: %s", want, table)
		}
	}
	if strings.Contains(string(table), token) {
		t.Fatalf("ask-table.md has the token: %s", table)
	}
}

// A later commit on go.mod (a pin or a rebase) must show in the table, not only the first bump.
func TestAskTableShowsLaterGoModChange(t *testing.T) {
	repo := askRepo(t)
	commitGoMod(t, repo, "require example.com/new v2.0.0", "chore(deps): bump Go dependencies")
	commitGoMod(t, repo, "require example.com/pinned v1.9.0", "fix: pin a module")
	key, runDir, exit := runAskTable(t, repo)
	if exit != 0 || key != "ok" {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	table, err := os.ReadFile(filepath.Join(runDir, "ask-table.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+require example.com/new v2.0.0", "+require example.com/pinned v1.9.0"} {
		if !strings.Contains(string(table), want) {
			t.Fatalf("ask-table.md lacks %q: %s", want, table)
		}
	}
}

func TestAskTableNoRefFails(t *testing.T) {
	repo := bumpRepo(t)
	key, _, exit := runAskTable(t, repo)
	if exit != 0 || key != "fail" {
		t.Fatalf("key=%q exit=%d, want fail", key, exit)
	}
}
