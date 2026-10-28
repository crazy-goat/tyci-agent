package daemon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/orchestrator"
)

// FixCIWorkflow is the workflow of a ci_failure trigger. It fixes a red
// default branch.
const FixCIWorkflow = "fix-ci"

// IssuePoller gives a trigger for each open issue of the lowest vX.Y.Z
// milestone that has the accepted label and an author with write access (the
// rule of the orchestrator). An issue with any run file of the workflow is
// done already.
type IssuePoller struct {
	Repo Repo
	Home string
}

// Poll implements Poller.
func (p *IssuePoller) Poll(ctx context.Context) ([]Trigger, error) {
	f := p.Repo.Forge
	ms, err := f.Milestones(ctx)
	if err != nil {
		return nil, err
	}
	m, err := forge.LowestMilestone(ms)
	if errors.Is(err, forge.ErrNoMilestone) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if forge.MilestoneState(m) != nil {
		return nil, nil // release needed, or an empty milestone: nothing to work on
	}
	issues, err := f.Issues(ctx, m.Title)
	if err != nil {
		return nil, err
	}
	var permErr error
	eligible, _ := orchestrator.Eligible(issues, p.Repo.AcceptedLabel, func(u string) (bool, error) {
		ok, err := f.CanWrite(ctx, u)
		if err != nil && permErr == nil {
			permErr = err
		}
		return ok, err
	})
	runs := repoRuns(p.Home, p.Repo.Name)
	var out []Trigger
	for _, is := range eligible {
		if hasIssueRun(runs, p.Repo.Workflow, is.Number) {
			continue
		}
		out = append(out, Trigger{
			Kind: KindIssue, Repo: p.Repo.Name, Workflow: p.Repo.Workflow, Issue: is.Number,
			Key: KindIssue + ":" + p.Repo.Name + "#" + strconv.Itoa(is.Number),
		})
	}
	// A failed permission check skips the issue; the next poll tries it again.
	return out, permErr
}

// MaxFixCIRuns is the most fix-ci runs that the daemon starts between two
// green CI runs that it sees.
const MaxFixCIRuns = 3

// CIPoller gives a fix-ci trigger when the newest completed CI run of the
// default branch is red. It gives no trigger when:
//   - a fix-ci run has the same trigger key (the same head SHA), or
//   - a fix-ci run is running or paused, or
//   - since the last green CI run that the poller saw (or since the poller
//     started), MaxFixCIRuns fix-ci runs started or the newest one failed.
//
// Poll is not safe for concurrent use; one poll loop calls it.
type CIPoller struct {
	Repo Repo
	Home string
	Now  func() time.Time
	Logf func(string, ...any)

	lastGreen time.Time
	blocked   string // the key that the last "no fix-ci" log line was for
}

// Poll implements Poller.
func (p *CIPoller) Poll(ctx context.Context) ([]Trigger, error) {
	if p.lastGreen.IsZero() {
		p.lastGreen = p.Now()
	}
	ci, ok, err := p.Repo.CI.LatestCI(ctx, p.Repo.CIWorkflow, p.Repo.DefaultBranch)
	if err != nil || !ok {
		return nil, err
	}
	if !ci.Failed {
		p.lastGreen = p.Now()
		return nil, nil
	}
	key := fmt.Sprintf("%s:%s:%s@%s", KindCIFailure, p.Repo.Name, p.Repo.DefaultBranch, ci.SHA)
	var (
		chain  int
		newest *flow.RunState
	)
	for _, st := range repoRuns(p.Home, p.Repo.Name) {
		if st.Workflow != FixCIWorkflow {
			continue
		}
		if st.Trigger == key || st.Status == "running" || st.Status == "paused" {
			return nil, nil
		}
		if st.StartedAt.Before(p.lastGreen) {
			continue
		}
		chain++
		if newest == nil || st.StartedAt.After(newest.StartedAt) {
			newest = st
		}
	}
	why := ""
	switch {
	case chain >= MaxFixCIRuns:
		why = fmt.Sprintf("%d fix-ci runs started since the last green CI run", chain)
	case newest != nil && (newest.Status == "failed" || newest.Status == flow.StatusStopped):
		why = fmt.Sprintf("the last fix-ci run %s is %s", flow.ShowRunID(newest.Run), newest.Status)
	}
	if why != "" {
		if p.blocked != key && p.Logf != nil {
			p.Logf("%s: no new fix-ci: %s; fix the default branch by hand (a green CI run or a daemon restart lets the daemon start fix-ci again)", key, why)
		}
		p.blocked = key
		return nil, nil
	}
	return []Trigger{{Kind: KindCIFailure, Repo: p.Repo.Name, Workflow: FixCIWorkflow, Key: key}}, nil
}

// cronPoller runs the cron tick in Poll. The tick starts the due jobs itself,
// so it gives no triggers.
type cronPoller func(ctx context.Context) error

// Poll implements Poller.
func (c cronPoller) Poll(ctx context.Context) ([]Trigger, error) { return nil, c(ctx) }

// repoRuns reads every run file of repo ("owner/name").
func repoRuns(home, repo string) []*flow.RunState {
	name := flow.RepoKey(repo)
	var out []*flow.RunState
	for _, st := range flow.RecentRuns(home, name, math.MaxInt) {
		if st.Repo == repo {
			out = append(out, st)
		}
	}
	return out
}

func hasIssueRun(runs []*flow.RunState, workflow string, issue int) bool {
	for _, st := range runs {
		if st.Workflow == workflow && st.Issue == issue {
			return true
		}
	}
	return false
}
