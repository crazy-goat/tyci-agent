// Package planner gives a planner session its own git worktree.
//
// A planner is a chat-driven agent that works on a plan, not on the code of a
// run. It has its own checkout, named plan-<id> (the folder and the branch have
// the same name), so it never shares files with a run or with the user's main
// checkout. The session does not count against the orchestrator's worker
// limit, because it does not take a run slot. Creating a session only adds a
// worktree; the package does not import internal/orchestrator or
// internal/flow. internal/flow imports internal/worktree, so the worktree
// package cannot import flow either.
//
// The worktree is created when the session starts. Close removes it when it
// holds no change, and keeps it otherwise.
package planner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/worktree"
)

// gitTimeout bounds the git call that reads the default branch.
const gitTimeout = 30 * time.Second

// Session is one planner session and its worktree.
type Session struct {
	// ID is the unique id of the session. The worktree and the branch are
	// both named plan-<ID>.
	ID string
	// Repo is the main checkout of the repository the session belongs to.
	Repo string
	// Worktree is the directory of the planner's checkout.
	Worktree string
	// Branch is the branch of the checkout, plan-<ID>.
	Branch string

	wt *worktree.Worktree
}

// New starts a planner session for repo. It makes a new id and creates the
// worktree at once. The worktree starts from origin/<default branch>, as the
// issue worktrees do, and lives under the user's ~/.tyci/worktrees.
func New(repo string) (*Session, error) {
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("planner: home directory: %w", err)
	}
	branch, err := defaultBranch(ctx, repo)
	if err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	wt, err := worktree.AddPlan(ctx, home, repo, id, branch)
	if err != nil {
		return nil, fmt.Errorf("planner: create worktree: %w", err)
	}
	return &Session{ID: id, Repo: wt.Repo, Worktree: wt.Dir, Branch: wt.Branch, wt: wt}, nil
}

// Close ends the session. When the worktree has no change, Close removes it and
// its branch and returns kept=false. Otherwise it keeps both, so the user can
// look at them, and returns kept=true. Close returns an error when the worktree
// cannot be checked, for example after its directory was deleted by hand.
func (s *Session) Close() (kept bool, err error) {
	ctx := context.Background()
	changed, err := s.wt.Changed(ctx)
	if err != nil {
		return true, fmt.Errorf("planner: check worktree: %w", err)
	}
	if changed {
		return true, nil
	}
	return false, s.wt.Remove(ctx)
}

// newID returns a random id of 16 hex digits.
func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("planner: random id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// defaultBranch reads the default branch of repo from refs/remotes/origin/HEAD.
// It repeats the lookup of flow.DetectRepoAt, because this package must not import flow.
func defaultBranch(ctx context.Context, repo string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").CombinedOutput()
	ref := strings.TrimSpace(string(out))
	if err != nil || !strings.HasPrefix(ref, "origin/") {
		return "", fmt.Errorf("planner: default branch unknown: run `git remote set-head origin --auto`")
	}
	return strings.TrimPrefix(ref, "origin/"), nil
}
