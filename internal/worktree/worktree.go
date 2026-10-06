// Package worktree gives an agent its own checkout of the repository.
//
// It exists because advisory locks are the wrong tool for parallel writes. Two
// children told to edit the same package must either take turns or have one of
// them refused by the write-freshness guard; neither is parallelism. A worktree
// removes the conflict instead of detecting it: each child gets a real
// directory with a real branch, edits whatever it likes, and nothing it does
// can be clobbered by a sibling.
//
// What it deliberately does NOT do is merge. A child's branch is left for
// someone to look at — the parent, or a person — because deciding that two
// independent changes compose is a judgement, not a file operation, and doing
// it silently is how a working tree ends up in a state nobody chose.
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitTimeout bounds a single git invocation. Creating a worktree is a local
// operation measured in milliseconds; anything approaching this is a lock held
// by another git process, and waiting minutes for it is worse than failing.
const gitTimeout = 30 * time.Second

// Worktree is one checkout, on its own branch.
type Worktree struct {
	// Dir is the checkout, and what an agent working here uses as its working
	// directory.
	Dir string
	// Branch is the branch created for it.
	Branch string
	// Repo is the repository it was added to.
	Repo string
	// BaseCommit is the commit the branch started from. Kept so Changed can
	// recognise work that was committed rather than left in the working tree.
	BaseCommit string
	// keepParent marks a worktree that lives in a shared directory: Remove
	// deletes only Dir, not its parent, so sibling worktrees survive.
	keepParent bool
}

// Add creates a worktree for repo on a new branch named after label.
//
// The branch starts from HEAD, so a child sees the same code the parent is
// looking at rather than whatever the default branch happens to hold.
func Add(ctx context.Context, repo, label string) (*Worktree, error) {
	root, err := Root(ctx, repo)
	if err != nil {
		return nil, err
	}

	base, err := git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("worktree: repository %s has no commits to branch from: %w", root, err)
	}

	branch := branchName(label)
	dir, err := os.MkdirTemp("", "tyci-worktree-")
	if err != nil {
		return nil, fmt.Errorf("worktree: temp dir: %w", err)
	}
	// git insists on creating the directory itself.
	target := filepath.Join(dir, filepath.Base(root))

	if out, err := git(ctx, root, "worktree", "add", "-b", branch, target, "HEAD"); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("worktree: git worktree add: %w: %s", err, out)
	}
	return &Worktree{Dir: target, Branch: branch, Repo: root, BaseCommit: strings.TrimSpace(base)}, nil
}

// AddIssue creates <home>/.tyci/worktrees/<repoName>/issue-<n> on branch
// issue-<n> from origin/<defaultBranch>. It fails if the directory or the
// branch already exists.
//
// home is a parameter so tests can pass t.TempDir(); callers pass the result
// of os.UserHomeDir. The branch starts from origin/<defaultBranch>, not HEAD,
// so the worktree holds the latest shared state rather than whatever the
// local checkout happens to point at.
func AddIssue(ctx context.Context, home, repo string, issue int, defaultBranch string) (*Worktree, error) {
	if issue <= 0 {
		return nil, fmt.Errorf("worktree: invalid issue number %d: must be positive", issue)
	}
	if defaultBranch == "" {
		return nil, fmt.Errorf("worktree: default branch must not be empty")
	}
	if home == "" {
		return nil, fmt.Errorf("worktree: home directory must not be empty")
	}

	root, err := Root(ctx, repo)
	if err != nil {
		return nil, err
	}

	branch := fmt.Sprintf("issue-%d", issue)
	target := filepath.Join(home, ".tyci", "worktrees", filepath.Base(root), branch)

	if _, err := os.Stat(target); err == nil {
		return nil, fmt.Errorf("worktree: %s already exists; remove it first (git worktree remove --force %s)", target, target)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("worktree: stat %s: %w", target, err)
	}

	if _, err := git(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return nil, fmt.Errorf("worktree: branch %s already exists; remove it first (git branch -D %s)", branch, branch)
	}

	// git creates the leaf itself; only the shared parents may be made here.
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, fmt.Errorf("worktree: mkdir parents of %s: %w", target, err)
	}

	if out, err := git(ctx, root, "fetch", "origin", defaultBranch); err != nil {
		return nil, fmt.Errorf("worktree: git fetch origin %s: %w: %s", defaultBranch, err, out)
	}

	if out, err := git(ctx, root, "worktree", "add", "-b", branch, target, "origin/"+defaultBranch); err != nil {
		return nil, fmt.Errorf("worktree: git worktree add: %w: %s", err, out)
	}

	base, err := git(ctx, root, "rev-parse", "origin/"+defaultBranch)
	if err != nil {
		// The worktree, its branch and its git registration already exist
		// at this point, but the caller gets no handle to Remove them with,
		// so clean up here and leave no state behind for a retry.
		_, _ = git(ctx, root, "worktree", "remove", "--force", target)
		_, _ = git(ctx, root, "branch", "-D", branch)
		_ = os.RemoveAll(target)
		return nil, fmt.Errorf("worktree: git rev-parse origin/%s: %w: %s", defaultBranch, err, base)
	}
	return &Worktree{Dir: target, Branch: branch, Repo: root, BaseCommit: strings.TrimSpace(base), keepParent: true}, nil
}

// Changed reports whether anything was actually modified in the worktree —
// tracked edits, staged changes or new untracked files.
//
// This is what decides whether a worktree is worth keeping. Most delegated
// work reads and reports; keeping a branch and a directory for every one of
// those would bury the ones that matter.
func (w *Worktree) Changed(ctx context.Context) (bool, error) {
	out, err := git(ctx, w.Dir, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("worktree: git status: %w: %s", err, out)
	}
	if strings.TrimSpace(out) != "" {
		return true, nil
	}
	// A committed change leaves a clean status but is still a change, so
	// compare against the commit the branch started from rather than trusting
	// the working tree alone.
	head, err := git(ctx, w.Dir, "rev-parse", "HEAD")
	if err != nil {
		// Treat an unreadable HEAD as unchanged: the dirty-status case above
		// is the one that matters, and guessing "changed" here would keep a
		// branch for every read-only task.
		return false, nil
	}
	return strings.TrimSpace(head) != w.BaseCommit, nil
}

// Remove deletes the worktree and its branch. Safe to call twice.
func (w *Worktree) Remove(ctx context.Context) error {
	if w == nil || w.Dir == "" {
		return nil
	}
	if out, err := git(ctx, w.Repo, "worktree", "remove", "--force", w.Dir); err != nil {
		// Fall through to the directory removal regardless: a half-removed
		// worktree left on disk is worse than a stale git registration, and
		// "worktree prune" cleans the latter up.
		_ = out
	}
	_, _ = git(ctx, w.Repo, "branch", "-D", w.Branch)
	if w.keepParent {
		// Issue worktrees share <home>/.tyci/worktrees/<repo> with their
		// siblings; only the leaf belongs to this worktree.
		return os.RemoveAll(w.Dir)
	}
	// The temp parent, not just the checkout git made inside it.
	return os.RemoveAll(filepath.Dir(w.Dir))
}

// Root returns the top level of the repository containing dir, or an error
// when dir is not in a git repository.
func Root(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("worktree: %s is not a git repository (isolation needs one): %w", dir, err)
	}
	return strings.TrimSpace(out), nil
}

// branchName turns a task label into something git will accept, and keeps it
// recognisable in `git branch` afterwards.
func branchName(label string) string {
	var b strings.Builder
	b.WriteString("tyci/")
	dashed := false
	for _, r := range strings.ToLower(label) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dashed = false
		default:
			if !dashed && b.Len() > len("tyci/") {
				b.WriteByte('-')
				dashed = true
			}
		}
		if b.Len() > 48 {
			break
		}
	}
	name := strings.TrimRight(b.String(), "-")
	if name == "tyci/" || name == "tyci" {
		name = "tyci/task"
	}
	// A suffix so two children given the same task do not collide.
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%1e6)
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
