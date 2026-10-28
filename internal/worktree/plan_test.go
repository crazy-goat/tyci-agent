package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAddPlan_CreatesNamedWorktree(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	home := t.TempDir()

	wt, err := AddPlan(context.Background(), home, work, "abc123", "main")
	if err != nil {
		t.Fatalf("AddPlan: %v", err)
	}
	t.Cleanup(func() { _ = wt.Remove(context.Background()) })

	root, err := Root(context.Background(), work)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	want := filepath.Join(home, ".tyci", "worktrees", filepath.Base(root), "plan-abc123")
	if wt.Dir != want {
		t.Fatalf("Dir = %q, want %q", wt.Dir, want)
	}
	if wt.Branch != "plan-abc123" {
		t.Fatalf("Branch = %q, want plan-abc123", wt.Branch)
	}
	if !BranchExists(context.Background(), work, "plan-abc123") {
		t.Fatal("branch plan-abc123 does not exist")
	}
}

func TestAddPlan_IdempotentKeepsBaseCommit(t *testing.T) {
	work, origin := newRepoWithOrigin(t)
	home := t.TempDir()
	ctx := context.Background()

	first, err := AddPlan(ctx, home, work, "same", "main")
	if err != nil {
		t.Fatalf("AddPlan: %v", err)
	}
	t.Cleanup(func() { _ = first.Remove(context.Background()) })

	// Origin moves on: another clone pushes a commit, and work fetches it.
	gitRun(t, filepath.Dir(work), "clone", "-q", origin, "other")
	other := filepath.Join(filepath.Dir(work), "other")
	gitRun(t, other, "checkout", "-q", "main")
	commitFile(t, other, "z", "z", "commit Z (upstream)")
	gitRun(t, other, "push", "-q", "origin", "main")
	gitRun(t, work, "fetch", "-q", "origin", "main")

	second, err := AddPlan(ctx, home, work, "same", "main")
	if err != nil {
		t.Fatalf("second AddPlan: %v", err)
	}
	if second.Dir != first.Dir {
		t.Fatalf("Dir = %q, want %q", second.Dir, first.Dir)
	}
	if second.Branch != first.Branch {
		t.Fatalf("Branch = %q, want %q", second.Branch, first.Branch)
	}
	if second.BaseCommit != first.BaseCommit {
		t.Fatalf("BaseCommit = %s, want %s", second.BaseCommit, first.BaseCommit)
	}
	changed, err := second.Changed(ctx)
	if err != nil {
		t.Fatalf("Changed: %v", err)
	}
	if changed {
		t.Fatal("Changed = true for a worktree with no own commit")
	}
}

func TestAddPlan_IdempotentRemoveKeepsSiblings(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	home := t.TempDir()
	ctx := context.Background()

	issue, err := AddIssue(ctx, home, work, "", 5, "main", true)
	if err != nil {
		t.Fatalf("AddIssue: %v", err)
	}
	t.Cleanup(func() { _ = issue.Remove(context.Background()) })
	sibling, err := AddPlan(ctx, home, work, "b", "main")
	if err != nil {
		t.Fatalf("AddPlan b: %v", err)
	}
	t.Cleanup(func() { _ = sibling.Remove(context.Background()) })
	plan, err := AddPlan(ctx, home, work, "a", "main")
	if err != nil {
		t.Fatalf("AddPlan a: %v", err)
	}
	t.Cleanup(func() { _ = plan.Remove(context.Background()) })
	again, err := AddPlan(ctx, home, work, "a", "main")
	if err != nil {
		t.Fatalf("second AddPlan a: %v", err)
	}
	if err := again.Remove(ctx); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	for _, dir := range []string{issue.Dir, sibling.Dir} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("sibling %s is gone after Remove of the plan: %v", dir, err)
		}
	}
}

func TestAddPlan_PartialStateFails(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	home := t.TempDir()
	ctx := context.Background()

	root, err := Root(ctx, work)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	// A directory without a branch is not a plan worktree: it must not be reused.
	stale := filepath.Join(home, ".tyci", "worktrees", filepath.Base(root), "plan-stale")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := AddPlan(ctx, home, work, "stale", "main"); err == nil {
		t.Fatal("AddPlan over a directory without a branch succeeded, want an error")
	}
}

func TestAddPlan_RejectsEmptyArgs(t *testing.T) {
	work, _ := newRepoWithOrigin(t)
	home := t.TempDir()
	ctx := context.Background()

	cases := []struct {
		name, home, id, branch string
	}{
		{"empty id", home, "", "main"},
		{"empty home", "", "x", "main"},
		{"empty default branch", home, "x", ""},
	}
	for _, c := range cases {
		if _, err := AddPlan(ctx, c.home, work, c.id, c.branch); err == nil {
			t.Errorf("%s: AddPlan succeeded, want an error", c.name)
		}
	}
}
