package flow

import (
	"os"
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
// st.PR == 0 yields an empty TYCI_PR.
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
	if st != nil {
		issue = strconv.Itoa(st.Issue)
		branch = st.Branch
		repo = st.Repo
		worktree = st.Worktree
		state = st.Current
		if st.Visits != nil {
			visit = st.Visits[st.Current]
		}
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
	)
	return env
}
