package daemon

import (
	"context"
	"fmt"
	"sync"

	"github.com/crazy-goat/tyci-agent/internal/flow"
)

// ManagerRuns is Runs over a flow.Manager of one repository.
type ManagerRuns struct {
	m    *flow.Manager
	home string
	repo string

	mu      sync.Mutex
	waiting map[string]chan Result
	// ended keeps a result that came before Start returned the run id.
	ended map[string]Result
}

// NewManagerRuns subscribes to the run events of m. home and repo find the
// run files.
func NewManagerRuns(m *flow.Manager, home, repo string) *ManagerRuns {
	r := &ManagerRuns{m: m, home: home, repo: repo, waiting: map[string]chan Result{}, ended: map[string]Result{}}
	m.Subscribe(r.event)
	return r
}

func (r *ManagerRuns) event(ev flow.RunEvent) {
	if ev.Status == "running" {
		return
	}
	res := Result{Status: ev.Status, Reason: ev.Reason, PR: ev.PR}
	if ev.Status == "paused" {
		// The event has no ask text; the run file has it.
		if st, err := flow.Load(flow.RunDir(r.home, flow.RepoKey(r.repo), ev.Run)); err == nil && st.Ask != nil {
			res.Reason = st.Ask.Message
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.waiting[ev.Run]; ok {
		delete(r.waiting, ev.Run)
		ch <- res
		return
	}
	r.ended[ev.Run] = res
}

// Start implements Runs.
func (r *ManagerRuns) Start(ctx context.Context, t Trigger) (string, <-chan Result, error) {
	var (
		id  string
		err error
	)
	switch t.Kind {
	case KindIssue:
		id, _, err = r.m.StartIssue(ctx, t.Workflow, t.Issue)
	case KindCIFailure:
		id, _, err = r.m.Start(ctx, flow.StartRequest{Workflow: t.Workflow, Trigger: t.Key})
	case KindResume:
		id, err = t.Run, r.m.Resume(t.Run, "resume")
	default:
		err = fmt.Errorf("unknown trigger kind %q", t.Kind)
	}
	if err != nil {
		return "", nil, err
	}
	ch := make(chan Result, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if res, ok := r.ended[id]; ok {
		delete(r.ended, id)
		ch <- res
	} else {
		r.waiting[id] = ch
	}
	return id, ch, nil
}

// Interrupted implements Runs. It pauses the runs whose owner process is gone
// (flow.Manager.AskUnfinished) and returns a resume trigger for each of them.
// A run that was paused before stays paused: it waits for a human.
func (r *ManagerRuns) Interrupted() ([]Trigger, error) {
	stale, err := flow.ScanResumable(flow.RunsDir(r.home), r.repo)
	if err != nil {
		return nil, err
	}
	if len(stale) == 0 {
		return nil, nil
	}
	r.m.AskUnfinished()
	var out []Trigger
	for _, st := range stale {
		out = append(out, Trigger{Kind: KindResume, Repo: r.repo, Workflow: st.Workflow, Issue: st.Issue, Run: st.Run, Key: KindResume + ":" + r.repo + ":" + st.Run})
	}
	return out, nil
}
