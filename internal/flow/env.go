package flow

import (
	"os"
	"path/filepath"
	"strconv"
)

// buildCheckEnv builds the minimal environment for check scripts (SDR 5.3).
// Only PATH, HOME and LANG are inherited from the parent, plus
// GH_TOKEN/GITHUB_TOKEN when set there. Everything else is constructed:
// TYCI_REPO, TYCI_ISSUE, TYCI_BRANCH, TYCI_DEFAULT_BRANCH, TYCI_WORKTREE,
// TYCI_RUN_DIR, TYCI_STATE, TYCI_VISIT and TYCI_PR.
//
// The state name comes from st.Current and the visit count from
// st.Visits[current] (the caller increments before running).
// st.PR == 0 yields an empty TYCI_PR. The runner appends TYCI_ARTIFACT_DIR,
// the artifact dir of the step, and TYCI_REVIEW_DIR (see reviewDir), both
// empty when the runner has no run dir.
// TYCI_DEFAULT_BRANCH is the defaultBranch argument (Runner.DefaultBranch).
// Nothing else from the parent env leaks in.
func buildCheckEnv(st *RunState, s State, runDir, defaultBranch string) []string {
	_ = s
	env := make([]string, 0, 16)
	for _, k := range []string{"PATH", "HOME", "LANG"} {
		env = append(env, k+"="+os.Getenv(k))
	}
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			env = append(env, k+"="+v)
		}
	}
	issue := ""
	branch := ""
	repo := ""
	worktree := ""
	state := ""
	visit := 0
	pr := ""
	lastComment := "0"
	lastReview := "0"
	if st != nil {
		issue = strconv.Itoa(st.Issue)
		branch = st.Branch
		repo = st.Repo
		worktree = st.Worktree
		state = st.Current
		if st.Visits != nil {
			visit = st.Visits[st.Current]
		}
		lastComment = strconv.FormatInt(st.LastCommentID, 10)
		lastReview = strconv.FormatInt(st.LastReviewCommentID, 10)
		if st.PR != 0 {
			pr = strconv.Itoa(st.PR)
		}
	}
	env = append(env,
		"TYCI_REPO="+repo,
		"TYCI_ISSUE="+issue,
		"TYCI_BRANCH="+branch,
		"TYCI_DEFAULT_BRANCH="+defaultBranch,
		"TYCI_WORKTREE="+worktree,
		"TYCI_RUN_DIR="+runDir,
		"TYCI_STATE="+state,
		"TYCI_VISIT="+strconv.Itoa(visit),
		"TYCI_PR="+pr,
		"TYCI_LAST_COMMENT_ID="+lastComment,
		"TYCI_LAST_REVIEW_COMMENT_ID="+lastReview,
	)
	return env
}

// reviewDir returns the artifact dir of the newest review step of the run: an
// agent state with agent "review" and no task (the verdict). The state name
// does not matter. Empty when the run has no such step, for example a run that
// continued an open PR. A task of the review role (findings) is not a review.
func (r *Runner) reviewDir(st *RunState) string {
	for i := len(st.History) - 1; i >= 0; i-- {
		h := st.History[i]
		s := r.WF.States[h.State]
		if s.Agent == "review" && s.Task == "" && h.Artifact != "" {
			return filepath.Join(r.RunDir, "artifacts", h.Artifact)
		}
	}
	return ""
}
