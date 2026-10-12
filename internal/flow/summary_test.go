package flow

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo makes a repo in dir with origin/main at the first commit, one more
// commit, and one uncommitted change to a tracked file.
func summaryRepo(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.invalid")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "first commit")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "b.txt")
	run("commit", "-q", "-m", "second commit")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryContents(t *testing.T) {
	dir := t.TempDir()
	summaryRepo(t, dir)
	s, err := BuildSummary(dir, StoppedAgent{Role: "worker", Reason: "no ping", DefaultBranch: "main",
		Progress: []string{"progress one", "progress two"}})
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	for _, want := range []string{"a.txt", "second commit", "progress one", "progress two", "Changes in the worktree", "Commits on this branch"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary lacks %q:\n%s", want, s)
		}
	}
	if len(s) > summaryCap {
		t.Errorf("summary is %d bytes, cap %d", len(s), summaryCap)
	}
}

func TestSummaryFirstLine(t *testing.T) {
	dir := t.TempDir()
	summaryRepo(t, dir)
	s, err := BuildSummary(dir, StoppedAgent{Role: "worker", Reason: "stuck for 10 minutes", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	first := strings.SplitN(s, "\n", 2)[0]
	if first != "Previous agent (role worker) was stopped: stuck for 10 minutes." {
		t.Fatalf("first line = %q", first)
	}
}

func TestSummaryCapDropsProgressFirst(t *testing.T) {
	dir := t.TempDir()
	summaryRepo(t, dir)
	var progress []string
	for i := 0; i < 20; i++ {
		progress = append(progress, fmt.Sprintf("p-%02d %s", i, strings.Repeat("x", 300)))
	}
	s, err := BuildSummary(dir, StoppedAgent{Role: "worker", Reason: "no ping", DefaultBranch: "main", Progress: progress})
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	if len(s) > summaryCap {
		t.Fatalf("summary is %d bytes, cap %d", len(s), summaryCap)
	}
	if !strings.Contains(s, "second commit") || !strings.Contains(s, "a.txt") {
		t.Fatalf("git sections were dropped before progress:\n%.300s", s)
	}
	if strings.Contains(s, "p-00 ") || !strings.Contains(s, "p-19 ") {
		t.Fatal("progress was not cut from the oldest line")
	}
}

func TestSummaryUsesExecDirNoShell(t *testing.T) {
	parent := t.TempDir()
	marker := filepath.Join(parent, "pwned")
	dir := filepath.Join(parent, "wt $(touch "+marker+") ; x")
	summaryRepo(t, dir)
	s, err := BuildSummary(dir, StoppedAgent{Role: "worker", Reason: "no ping", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("a shell ran the path")
	}
	if !strings.Contains(s, "a.txt") {
		t.Fatalf("git ran in the wrong directory:\n%s", s)
	}
}
