package flow

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// summaryCap is the byte limit of a summary of a replaced agent (#202).
const summaryCap = 4096

// summaryProgress is how many progress lines a summary keeps.
const summaryProgress = 20

// summaryGitTimeout bounds each git call of a summary.
const summaryGitTimeout = 10 * time.Second

// StoppedAgent is the agent that a replacement stops. BuildSummary reads it.
type StoppedAgent struct {
	Role          string   // role of the stopped agent, for the first line
	Reason        string   // why it was stopped
	DefaultBranch string   // the commits are origin/<DefaultBranch>..HEAD
	Progress      []string // progress lines, oldest first; only the last 20 are used
}

// BuildSummary returns the text that the new agent gets about the stopped agent.
// The first line is "Previous agent (role <role>) was stopped: <reason>.". Then
// come the git diff --stat and the git log of the worktree, and the last progress
// lines. git runs in worktree with exec, not through a shell. The text is at most
// summaryCap bytes: the progress lines go first, then the end of the text. An
// error from git does not stop the summary; the failed section says so, and the
// error is returned too.
func BuildSummary(worktree string, a StoppedAgent) (string, error) {
	first := fmt.Sprintf("Previous agent (role %s) was stopped: %s.", a.Role, strings.TrimRight(a.Reason, "."))
	var errs []error
	stat, err := gitOutput(worktree, "diff", "--stat")
	if err != nil {
		errs = append(errs, err)
		stat = "(git diff failed: " + err.Error() + ")"
	}
	log, err := gitOutput(worktree, "log", "--oneline", "origin/"+a.DefaultBranch+"..HEAD")
	if err != nil {
		errs = append(errs, err)
		log = "(git log failed: " + err.Error() + ")"
	}
	progress := a.Progress
	if len(progress) > summaryProgress {
		progress = progress[len(progress)-summaryProgress:]
	}
	for {
		s := assembleSummary(first, stat, log, progress)
		if len(s) <= summaryCap {
			return s, errors.Join(errs...)
		}
		if len(progress) == 0 {
			return cutBytes(s, summaryCap), errors.Join(errs...)
		}
		progress = progress[1:]
	}
}

// assembleSummary joins the sections of a summary.
func assembleSummary(first, stat, log string, progress []string) string {
	var b strings.Builder
	b.WriteString(first)
	b.WriteString("\n\nChanges in the worktree (git diff --stat):\n")
	b.WriteString(orNone(stat))
	b.WriteString("\n\nCommits on this branch (git log --oneline):\n")
	b.WriteString(orNone(log))
	b.WriteString("\n\nLast progress lines:\n")
	if len(progress) == 0 {
		b.WriteString("(none)")
	}
	for _, line := range progress {
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// cutBytes cuts s to at most n bytes without splitting a UTF-8 character.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// gitOutput runs git in dir without a shell and returns the trimmed stdout.
func gitOutput(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), summaryGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
