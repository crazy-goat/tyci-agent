package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/instructions"
)

// subagentWorktree builds a main checkout with a linked worktree and returns
// both paths. HOME is a temp dir so the test never reads the real ~/.tyci.
func subagentWorktree(t *testing.T) (main, wt string) {
	t.Helper()
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
	main = filepath.Join(tmp, "main")
	wt = filepath.Join(tmp, "wt")
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
	return main, wt
}

// TestSubagentWorkdirReadsMainAgentsAndSkills checks that a subagent running
// in a linked worktree finds the main checkout's named agents and skills.
func TestSubagentWorkdirReadsMainAgentsAndSkills(t *testing.T) {
	main, wt := subagentWorktree(t)
	writeTestFile(t, filepath.Join(main, ".tyci", "agents", "reviewer.md"), "---\ndescription: reviews code\n---\n\nReview body.\n")
	writeTestFile(t, filepath.Join(main, ".tyci", "skills", "release", "SKILL.md"), "---\nname: release\ndescription: release steps\n---\nSteps.\n")

	ctx := WithWorkdir(context.Background(), wt)
	res := (&AgentsTool{}).Run(ctx, map[string]any{"action": "list"})
	if !strings.Contains(res.Content, "reviewer") {
		t.Fatalf("agents list from the worktree does not show the main agent: %q", res.Content)
	}
	skill := (&SkillsTool{}).Run(ctx, map[string]any{"name": "release"})
	if !skill.Success {
		t.Fatalf("skill from the worktree not found in main: %s", skill.Error)
	}
}

// TestSubagentWorkdirMemoryWriteLandsInMain checks that a memory note written
// from a worktree is stored in the main checkout's .tyci/memory.
func TestSubagentWorkdirMemoryWriteLandsInMain(t *testing.T) {
	main, wt := subagentWorktree(t)
	ctx := WithWorkdir(context.Background(), wt)

	res := (&MemoryTool{}).Run(ctx, map[string]any{
		"action":  "write",
		"name":    "Worktree Note",
		"content": "written from a worktree",
	})
	if !res.Success {
		t.Fatalf("write failed: %s", res.Error)
	}
	onDisk := filepath.Join(instructions.MemoryDir(main), "worktree-note.md")
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("note not in the main checkout: %v", err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
