package flow

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A workflow proposal (#372) is proposal.md and proposal.patch in the artifact
// dir of a fixer step: a change of the repository's .tyci/ files. It changes
// nothing until the user answers "apply"; "reject" hides it for good.

// rejectedProposals is the file, next to the run dirs of a repository, with
// one sha256 of a rejected proposal.patch per line.
const rejectedProposals = "rejected-proposals"

// proposalWaits starts the part of a pause message about a proposal.
const proposalWaits = " A workflow proposal waits: "

// findProposal returns the artifact dir of the newest fixer step since the
// last answered ask that has a non-empty proposal.patch, or "". A rejected
// proposal is skipped.
func findProposal(st *RunState, runDir string) string {
	if runDir == "" {
		return ""
	}
	for i := len(st.History) - 1; i >= 0; i-- {
		h := st.History[i]
		if h.Kind == "ask" {
			return ""
		}
		if h.Role != "fixer" || h.Artifact == "" {
			continue
		}
		dir := filepath.Join(runDir, "artifacts", h.Artifact)
		key, err := proposalKey(dir)
		if err != nil || isRejected(runDir, key) {
			continue
		}
		return dir
	}
	return ""
}

// proposalKey is the sha256 of proposal.patch. An empty patch is an error.
func proposalKey(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "proposal.patch"))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(b)) == "" {
		return "", errors.New("proposal.patch is empty")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func isRejected(runDir, key string) bool {
	f, err := os.Open(filepath.Join(filepath.Dir(runDir), rejectedProposals))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == key {
			return true
		}
	}
	return false
}

// RejectProposal records the proposal in dir as rejected for the repository
// of runDir, so no later pause shows it again.
func RejectProposal(runDir, dir string) error {
	key, err := proposalKey(dir)
	if err != nil {
		return err
	}
	if isRejected(runDir, key) {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(runDir), rejectedProposals), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, key); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// proposalSummary returns the first non-empty line of proposal.md.
func proposalSummary(dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "proposal.md"))
	return strings.TrimLeft(firstLine(string(b)), "# ")
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// ApplyProposal applies the proposal in dir to the repository in a new branch
// tyci/proposal-<run>-<patch hash>, made from origin/<default branch> in a temporary
// worktree, pushes it and opens a PR. When the repository has no local copy
// of the workflow yet, the missing files of the builtin one are ejected first
// (existing .tyci/ files and role prompts are kept). A patch that touches
// a file outside .tyci/ is refused. The checkout of the user is not touched.
// It returns the PR URL.
func ApplyProposal(ctx context.Context, info RepoInfo, st *RunState, dir string) (string, error) {
	patch := filepath.Join(dir, "proposal.patch")
	key, err := proposalKey(dir)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, "proposal.md")); err != nil {
		return "", err
	}
	git := func(wd string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = wd
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := git(info.Root, "fetch", "origin", info.DefaultBranch); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "tyci-proposal-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	wt := filepath.Join(tmp, "wt")
	branch := "tyci/proposal-" + st.Run + "-" + key[:8]
	if _, err := git(info.Root, "worktree", "add", "-b", branch, wt, "origin/"+info.DefaultBranch); err != nil {
		return "", err
	}
	defer func() {
		_, _ = git(info.Root, "worktree", "remove", "--force", wt)
		_, _ = git(info.Root, "branch", "-D", branch)
	}()
	if _, err := os.Stat(filepath.Join(wt, ".tyci", "workflows", st.Workflow+".json")); err != nil {
		if _, err := EjectMissing(st.Workflow, wt); err != nil {
			return "", fmt.Errorf("eject %s: %w", st.Workflow, err)
		}
		if _, err := git(wt, "add", "-A", ".tyci"); err != nil {
			return "", err
		}
	}
	if _, err := git(wt, "apply", "--index", patch); err != nil {
		return "", err
	}
	// --no-renames: a rename into .tyci/ must also list its source path.
	names, err := git(wt, "diff", "--cached", "--name-only", "--no-renames")
	if err != nil {
		return "", err
	}
	if names == "" {
		return "", errors.New("the proposal changes nothing")
	}
	for _, n := range strings.Split(names, "\n") {
		if !strings.HasPrefix(n, ".tyci/") {
			return "", fmt.Errorf("the proposal changes %s, outside .tyci/", n)
		}
	}
	title := "chore(workflow): apply the workflow proposal of run " + st.Run
	if _, err := git(wt, "commit", "-q", "-m", title); err != nil {
		return "", err
	}
	if _, err := git(wt, "push", "origin", "refs/heads/"+branch+":refs/heads/"+branch); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "gh", "pr", "create", "-R", info.Repo, "--base", info.DefaultBranch,
		"--head", branch, "--title", title, "--body-file", filepath.Join(dir, "proposal.md"))
	cmd.Dir = wt
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Remove the pushed branch, so a later apply can push it again.
		_, _ = git(wt, "push", "origin", "--delete", "refs/heads/"+branch)
		return "", fmt.Errorf("gh pr create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return lastLine(string(out)), nil
}
