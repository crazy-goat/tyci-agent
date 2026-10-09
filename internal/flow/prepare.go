package flow

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/worktree"
)

// PrepareDeps holds the steps PrepareRun calls, so tests can fake them.
type PrepareDeps struct {
	Lookup func(name string) (*Workflow, string, error) // workflow + source
	Config func() (*flowconfig.Config, error)
	// AddWorktree creates the worktree of the run. issue is 0 when the workflow has
	// no issue param; the worktree is then named after runID.
	AddWorktree func(ctx context.Context, runID string, issue int) (*worktree.Worktree, error)
	NewStore    func(runID string) (*Store, error)
}

// PrepareReq says which run to prepare.
type PrepareReq struct {
	Workflow, Repo, DefaultBranch string
	// Params are the positional values of the run (see BindParams).
	Params []string
}

// PrepareRun does: 1 Lookup, 2 Config, 3 Validate and BindParams (any error
// returns and creates NOTHING), 4 AddWorktree (worktree), 5 NewStore and the
// initial RunState (status running, current = start), saved once.
func PrepareRun(ctx context.Context, d PrepareDeps, req PrepareReq) (*RunState, *Workflow, []string, error) {
	wf, _, err := d.Lookup(req.Workflow)
	if err != nil {
		return nil, nil, nil, err
	}
	cfg, err := d.Config()
	if err != nil {
		return nil, nil, nil, err
	}
	warnings, err := Validate(wf, cfg, wfResolver(wf))
	if err != nil {
		return nil, nil, warnings, fmt.Errorf("workflow %q is invalid: %w", req.Workflow, err)
	}
	params, err := BindParams(wf, req.Params)
	if err != nil {
		return nil, nil, warnings, err
	}
	now := time.Now().UTC()
	issue := issueOf(params)
	runID := NewRunID(issue, now)
	wt, err := d.AddWorktree(ctx, runID, issue)
	if err != nil {
		return nil, nil, warnings, err
	}
	store, err := d.NewStore(runID)
	if err != nil {
		return nil, nil, warnings, err
	}
	st := &RunState{
		Version:   1,
		Run:       runID,
		Workflow:  wf.Name,
		Repo:      req.Repo,
		Issue:     issue,
		Params:    params,
		Branch:    wt.Branch,
		Worktree:  wt.Dir,
		Status:    "running",
		PID:       os.Getpid(),
		Current:   wf.Start,
		StartedAt: now,
		Visits:    map[string]int{},
		History:   []Step{},
	}
	if err := store.Save(st); err != nil {
		return nil, nil, warnings, err
	}
	return st, wf, warnings, nil
}
