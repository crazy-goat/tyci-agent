package daemon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

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

// CIPoller gives a fix-ci trigger when the newest completed CI run of the
// default branch is red and no fix-ci run started after that CI run.
type CIPoller struct {
	Repo Repo
	Home string
}

// Poll implements Poller.
func (p *CIPoller) Poll(ctx context.Context) ([]Trigger, error) {
	ci, ok, err := p.Repo.CI.LatestCI(ctx, p.Repo.CIWorkflow, p.Repo.DefaultBranch)
	if err != nil || !ok || !ci.Failed {
		return nil, err
	}
	for _, st := range repoRuns(p.Home, p.Repo.Name) {
		if st.Workflow != FixCIWorkflow {
			continue
		}
		// An open fix-ci run, or one that started after the red CI run, covers it.
		if st.Status == "running" || st.Status == "paused" || !st.StartedAt.Before(ci.CreatedAt) {
			return nil, nil
		}
	}
	return []Trigger{{
		Kind: KindCIFailure, Repo: p.Repo.Name, Workflow: FixCIWorkflow,
		Key: fmt.Sprintf("%s:%s:%s@%s", KindCIFailure, p.Repo.Name, p.Repo.DefaultBranch, ci.SHA),
	}}, nil
}

// cronPoller runs the cron tick in Poll. The tick starts the due jobs itself,
// so it gives no triggers.
type cronPoller func(ctx context.Context) error

// Poll implements Poller.
func (c cronPoller) Poll(ctx context.Context) ([]Trigger, error) { return nil, c(ctx) }

// repoRuns reads every run file of repo ("owner/name").
func repoRuns(home, repo string) []*flow.RunState {
	name := flow.RepoInfo{Repo: repo}.Name()
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
