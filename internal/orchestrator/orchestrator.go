package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/forge"
)

// Config is the orchestrator configuration.
type Config struct {
	Workers       int           // 0 = unlimited
	PlanTimeout   time.Duration // 0 = none
	Workflow      string        // "" -> "issue-to-merge"
	AcceptedLabel string        // "" -> "accepted"
}

// PlanReady is the hook the greeting uses. The orchestrator never formats the greeting.
type PlanReady struct {
	Roadmap Roadmap
	Free    int   // free slots after the fill; -1 when unlimited
	Total   int   // Config.Workers; 0 = unlimited
	Started []int // issues started by the fill
	Special error // forge.ErrNoMilestone / ErrReleaseNeeded / ErrMilestoneEmpty / forge error / nil
}

// Hooks are the callbacks of the orchestrator.
type Hooks struct {
	Notify    func(line string) // one-line chat notices
	PlanReady func(PlanReady)   // called once per plan (initial, and the one re-plan)
}

type finishedRun struct {
	issue int
	ask   bool
	res   RunResult
}

// Orchestrator plans a milestone and runs one workflow run per issue. It uses
// no model itself. One mutex guards all state.
type Orchestrator struct {
	cfg Config
	f   forge.Forge
	r   Runner
	h   Hooks

	mu       sync.Mutex
	roadmap  Roadmap
	inFlight map[int]bool
	announce bool // send "started #N" notices (false until the first fill is done)
	stop     chan struct{}
}

// New returns an Orchestrator. Workers is not defaulted: 0 means unlimited.
func New(cfg Config, f forge.Forge, r Runner, h Hooks) *Orchestrator {
	if cfg.Workflow == "" {
		cfg.Workflow = "issue-to-merge"
	}
	if cfg.AcceptedLabel == "" {
		cfg.AcceptedLabel = "accepted"
	}
	return &Orchestrator{cfg: cfg, f: f, r: r, h: h, inFlight: map[int]bool{}, stop: make(chan struct{})}
}

// Start runs the orchestrator in a goroutine and returns at once. Cancel ctx
// to stop starting new runs; started runs continue.
func (o *Orchestrator) Start(ctx context.Context) {
	go func() {
		defer close(o.stop)
		defer func() {
			if p := recover(); p != nil {
				o.notify(fmt.Sprintf("orchestrator stopped: panic: %v", p))
			}
		}()
		o.loop(ctx)
	}()
}

// Roadmap returns a copy of the plan.
func (o *Orchestrator) Roadmap() Roadmap {
	o.mu.Lock()
	defer o.mu.Unlock()
	return copyRoadmap(o.roadmap)
}

func copyRoadmap(r Roadmap) Roadmap {
	r.Items = append([]Item(nil), r.Items...)
	for i := range r.Items {
		r.Items[i].DependsOn = append([]int(nil), r.Items[i].DependsOn...)
	}
	r.Skipped = append([]Skipped(nil), r.Skipped...)
	return r
}

func (o *Orchestrator) notify(lines ...string) {
	if o.h.Notify == nil {
		return
	}
	for _, l := range lines {
		o.h.Notify(l)
	}
}

func (o *Orchestrator) loop(ctx context.Context) {
	rm, err := o.plan(ctx)
	if err != nil {
		o.reportForgeError(err, PlanReady{Roadmap: rm, Total: o.cfg.Workers, Special: err})
		return
	}
	o.mu.Lock()
	o.roadmap = rm
	o.mu.Unlock()
	finished := make(chan finishedRun)
	replanned := false
	ready := true
	for {
		started := o.fill(ctx, finished)
		if ready {
			ready = false
			o.mu.Lock()
			o.announce = true
			o.mu.Unlock()
			o.planReady(started, nil)
		}
		if o.nothingLeft() && ctx.Err() == nil {
			again, stop := o.drain(ctx, replanned)
			if again {
				replanned, ready = true, true
				continue
			}
			if stop {
				return
			}
		}
		select {
		case f := <-finished:
			o.finish(f)
		case <-ctx.Done():
			return
		}
	}
}

func (o *Orchestrator) reportForgeError(err error, pr PlanReady) {
	if o.h.PlanReady != nil {
		o.h.PlanReady(pr) // the owner formats the special case
		return
	}
	if errors.Is(err, forge.ErrReleaseNeeded) {
		o.notify(fmt.Sprintf("Milestone %s has no open issues left. Release needed (ask me, I do not release yet).", pr.Roadmap.Milestone))
	} else {
		o.notify("forge error: " + err.Error())
	}
}

func (o *Orchestrator) planReady(started []int, special error) {
	if o.h.PlanReady == nil {
		return
	}
	o.mu.Lock()
	pr := PlanReady{Roadmap: copyRoadmap(o.roadmap), Total: o.cfg.Workers, Started: started, Special: special, Free: -1}
	if o.cfg.Workers > 0 {
		pr.Free = o.cfg.Workers - len(o.inFlight)
	}
	o.mu.Unlock()
	o.h.PlanReady(pr)
}

// gather reads the forge and builds the oracle input. skip lists issues that
// are already in the plan; they are left out of the input.
func (o *Orchestrator) gather(ctx context.Context, skip map[int]bool) (Roadmap, Input, error) {
	ms, err := o.f.Milestones(ctx)
	if err != nil {
		return Roadmap{}, Input{}, err
	}
	m, err := forge.LowestMilestone(ms)
	if err != nil {
		return Roadmap{}, Input{}, err
	}
	if err := forge.MilestoneState(m); err != nil {
		return Roadmap{Repo: o.f.Repo(), Milestone: m.Title}, Input{}, err
	}
	issues, err := o.f.Issues(ctx, m.Title)
	if err != nil {
		return Roadmap{}, Input{}, err
	}
	none, err := o.f.Issues(ctx, "")
	if err != nil {
		return Roadmap{}, Input{}, err
	}
	var fresh []forge.Issue
	for _, is := range issues {
		if !skip[is.Number] {
			fresh = append(fresh, is)
		}
	}
	in, skipped := buildInput(o.f.Repo(), m.Title, o.cfg.AcceptedLabel, fresh, func(u string) (bool, error) { return o.f.CanWrite(ctx, u) })
	rm := Roadmap{Repo: o.f.Repo(), Milestone: m.Title, Skipped: skipped}
	rm.Counts = Counts{MilestoneOpen: len(issues), MilestoneAccepted: len(in.Issues) + len(skip), NoMilestone: len(none)}
	return rm, in, nil
}

// plan reads the forge and orders the issues. It returns an error only for forge errors; an oracle failure uses the
// fallback order.
func (o *Orchestrator) plan(ctx context.Context) (Roadmap, error) {
	rm, in, err := o.gather(ctx, nil)
	if err != nil {
		return rm, err
	}
	rm.Items, rm.FromOracle, rm.FallbackWhy = o.order(ctx, in)
	return rm, nil
}

func (o *Orchestrator) order(ctx context.Context, in Input) (items []Item, fromOracle bool, why string) {
	raw, err := json.Marshal(in)
	if err != nil {
		return fallbackOrder(in), false, "cannot encode input"
	}
	res, err := o.runOracle(ctx, string(raw))
	if err == nil {
		items, err = parseRoadmap(res, in)
	}
	if err != nil {
		return fallbackOrder(in), false, shortWhy(err)
	}
	return items, true, ""
}

func shortWhy(err error) string {
	s := strings.SplitN(err.Error(), "\n", 2)[0]
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func (o *Orchestrator) runOracle(ctx context.Context, input string) (string, error) {
	h, err := o.r.Start(ctx, "roadmap", map[string]string{"input": input})
	if err != nil {
		return "", err
	}
	var timeout <-chan time.Time
	if o.cfg.PlanTimeout > 0 {
		t := time.NewTimer(o.cfg.PlanTimeout)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case res := <-h.Done():
		if res.Err != nil {
			return "", res.Err
		}
		if res.Outcome != "ended" && res.Outcome != "merged" {
			return "", fmt.Errorf("oracle run %s", res.Outcome)
		}
		return res.Output, nil
	case <-timeout:
		return "", errors.New("oracle run timed out")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (o *Orchestrator) item(issue int) *Item {
	for i := range o.roadmap.Items {
		if o.roadmap.Items[i].Issue == issue {
			return &o.roadmap.Items[i]
		}
	}
	return nil
}

// depState returns the status of a dependency. An issue that is not in the
// plan (already merged and closed) counts as done.
func (o *Orchestrator) depState(issue int) ItemStatus {
	if it := o.item(issue); it != nil {
		return it.Status
	}
	return StatusDone
}

// fill starts every ready item while a slot is free. It returns the started issues.
func (o *Orchestrator) fill(ctx context.Context, finished chan<- finishedRun) []int {
	var started []int
	var notes []string
	o.mu.Lock()
	notes = append(notes, o.markBlocked()...)
	for i := range o.roadmap.Items {
		if ctx.Err() != nil {
			break
		}
		it := &o.roadmap.Items[i]
		if it.Status != StatusTodo || o.inFlight[it.Issue] || !o.ready(it) {
			continue
		}
		if o.cfg.Workers > 0 && len(o.inFlight) >= o.cfg.Workers {
			break
		}
		o.inFlight[it.Issue] = true
		it.Status = StatusWip
		h, err := o.r.Start(ctx, o.cfg.Workflow, map[string]string{"issue": strconv.Itoa(it.Issue)})
		if err != nil {
			delete(o.inFlight, it.Issue)
			it.Status = StatusFailed
			notes = append(notes, fmt.Sprintf("#%d failed: %v; slot freed", it.Issue, err))
			notes = append(notes, o.markBlocked()...)
			continue
		}
		it.RunID = h.ID()
		started = append(started, it.Issue)
		if o.announce {
			notes = append(notes, fmt.Sprintf("started #%d (run %s)", it.Issue, it.RunID))
		}
		go o.watch(it.Issue, h, finished)
	}
	o.mu.Unlock()
	o.notify(notes...)
	return started
}

func (o *Orchestrator) ready(it *Item) bool {
	for _, d := range it.DependsOn {
		if o.depState(d) != StatusDone {
			return false
		}
	}
	return true
}

// markBlocked blocks todo items that depend on a failed or blocked item and
// returns one notice per item. o.mu must be held.
func (o *Orchestrator) markBlocked() []string {
	var notes []string
	for changed := true; changed; {
		changed = false
		for i := range o.roadmap.Items {
			it := &o.roadmap.Items[i]
			if it.Status != StatusTodo {
				continue
			}
			for _, d := range it.DependsOn {
				if s := o.depState(d); s == StatusFailed || s == StatusBlocked {
					it.Status = StatusBlocked
					notes = append(notes, fmt.Sprintf("#%d blocked: #%d did not finish", it.Issue, d))
					changed = true
					break
				}
			}
		}
	}
	return notes
}

func (o *Orchestrator) watch(issue int, h RunHandle, finished chan<- finishedRun) {
	send := func(f finishedRun) bool {
		select {
		case finished <- f:
			return true
		case <-o.stopped():
			return false
		}
	}
	for {
		select {
		case <-h.Asks():
			if !send(finishedRun{issue: issue, ask: true}) {
				return
			}
		case res := <-h.Done():
			send(finishedRun{issue: issue, res: res})
			return
		}
	}
}

// stopped is closed when the loop has returned.
func (o *Orchestrator) stopped() <-chan struct{} { return o.stop }

func (o *Orchestrator) finish(f finishedRun) {
	o.mu.Lock()
	it := o.item(f.issue)
	var notes []string
	switch {
	case it == nil:
	case f.ask:
		// The run is alive and keeps its worktree and its slot.
		it.Status = StatusAsk
		notes = append(notes, fmt.Sprintf("#%d waits for your answer", f.issue))
	default:
		delete(o.inFlight, f.issue)
		switch f.res.Outcome {
		case "merged":
			it.Status = StatusDone
			notes = append(notes, fmt.Sprintf("#%d merged", f.issue))
		case "ended":
			it.Status = StatusDone
			notes = append(notes, fmt.Sprintf("#%d ended", f.issue))
		default:
			it.Status = StatusFailed
			why := f.res.Outcome
			if f.res.Err != nil {
				why = f.res.Err.Error()
			}
			notes = append(notes, fmt.Sprintf("#%d failed: %s; slot freed", f.issue, why))
		}
		notes = append(notes, o.markBlocked()...)
	}
	o.mu.Unlock()
	o.notify(notes...)
}

// nothingLeft is true when no item runs, waits for an answer or can start.
func (o *Orchestrator) nothingLeft() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := range o.roadmap.Items {
		it := &o.roadmap.Items[i]
		switch it.Status {
		case StatusWip, StatusAsk:
			return false
		case StatusTodo:
			if o.ready(it) {
				return false
			}
		}
	}
	return true
}

// drain re-reads the milestone once. It returns again=true after a re-plan
// that added items, and stop=true when the loop must end.
func (o *Orchestrator) drain(ctx context.Context, replanned bool) (again, stop bool) {
	o.mu.Lock()
	known := map[int]bool{}
	for _, it := range o.roadmap.Items {
		known[it.Issue] = true
	}
	o.mu.Unlock()
	rm, in, err := o.gather(ctx, known)
	if err != nil {
		o.reportForgeError(err, PlanReady{Roadmap: rm, Total: o.cfg.Workers, Special: err})
		return false, true
	}
	if len(in.Issues) > 0 && !replanned {
		items, fromOracle, why := o.order(ctx, in)
		o.mu.Lock()
		base := len(o.roadmap.Items)
		for i := range items {
			items[i].Order = base + i + 1
		}
		o.roadmap.Items = append(o.roadmap.Items, items...)
		o.roadmap.Skipped = rm.Skipped
		o.roadmap.Counts = rm.Counts
		o.roadmap.FromOracle, o.roadmap.FallbackWhy = fromOracle, why
		o.mu.Unlock()
		return true, false
	}
	var bad []string
	o.mu.Lock()
	for _, it := range o.roadmap.Items {
		if it.Status == StatusFailed || it.Status == StatusBlocked {
			bad = append(bad, fmt.Sprintf("#%d (%s)", it.Issue, it.Status))
		}
	}
	o.mu.Unlock()
	if len(bad) > 0 {
		o.notify("Nothing more to start. Left failed or blocked: " + strings.Join(bad, ", "))
	} else {
		o.notify("Nothing more to start.")
	}
	return false, true
}
