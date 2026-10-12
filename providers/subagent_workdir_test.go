package providers

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSubagentPromptUsesWorktreeWorkdir checks that a subagent in a linked
// worktree reads the worktree's AGENTS.md and names the worktree as its
// working directory.
func TestSubagentPromptUsesWorktreeWorkdir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	tmp := t.TempDir()
	main := filepath.Join(tmp, "main")
	wt := filepath.Join(tmp, "wt")
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	git(main, "init", "-q", "-b", "main")
	git(main, "commit", "-q", "--allow-empty", "-m", "init")
	git(main, "worktree", "add", "-q", wt, "-b", "feature")
	if err := os.WriteFile(filepath.Join(wt, "AGENTS.md"), []byte("WORKTREE-RULE: use the worktree notes."), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := BuildSubagentSystemPromptWithRole("ROLE", true, wt)
	if !strings.Contains(prompt, "WORKTREE-RULE") {
		t.Fatal("the subagent prompt does not carry the worktree's AGENTS.md")
	}
	if !strings.Contains(prompt, wt) {
		t.Fatalf("the subagent prompt does not name the worktree %q", wt)
	}
}
