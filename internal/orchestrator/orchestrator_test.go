package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/forge/fake"
)

const wait = 5 * time.Second

type env struct {
	t      *testing.T
	o      *Orchestrator
	f      *fake.Fake
	r      *fakeRunner
	notes  chan string
	ready  chan PlanReady
	cancel context.CancelFunc
}

func issue(n int, body string) forge.Issue {
	return forge.Issue{Number: n, Title: fmt.Sprintf("issue %d", n), Labels: []string{"accepted"}, Author: "w", Milestone: "v0.4.0", Body: body, State: "open"}
}

func newEnv(t *testing.T, cfg Config, issues ...forge.Issue) *env {
	t.Helper()
	e := &env{t: t, r: newFakeRunner(), notes: make(chan string, 100), ready: make(chan PlanReady, 10)}
	e.f = &fake.Fake{RepoName: "o/r", Writers: map[string]bool{"w": true},
		Ms: []forge.Milestone{{Number: 1, Title: "v0.4.0", OpenCount: len(issues)}}, Is: issues}
	e.o = New(cfg, e.f, e.r, Hooks{
		Notify:    func(l string) { e.notes <- l },
		PlanReady: func(p PlanReady) { e.ready <- p },
	})
	return e
}

func (e *env) start() PlanReady {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.t.Cleanup(cancel)
	e.o.Start(ctx)
	p := e.waitReady()
	for len(e.r.started) > 0 { // the first starts are in PlanReady.Started
		<-e.r.started
	}
	return p
}

func (e *env) waitReady() PlanReady {
	e.t.Helper()
	select {
	case p := <-e.ready:
		return p
	case <-time.After(wait):
		e.t.Fatal("no PlanReady")
	}
	return PlanReady{}
}

func (e *env) waitStart() int {
	e.t.Helper()
	select {
	case n := <-e.r.started:
		return n
	case <-time.After(wait):
		e.t.Fatal("no run started")
	}
	return 0
}

func (e *env) waitNote(sub string) {
	e.t.Helper()
	for {
		select {
		case l := <-e.notes:
			if strings.Contains(l, sub) {
				return
			}
		case <-time.After(wait):
			e.t.Fatalf("no notice containing %q", sub)
		}
	}
}

func (e *env) waitStop() {
	e.t.Helper()
	select {
	case <-e.o.stop:
	case <-time.After(wait):
		e.t.Fatal("loop did not stop")
	}
}

func (e *env) status(n int) ItemStatus {
	for _, it := range e.o.Roadmap().Items {
		if it.Issue == n {
			return it.Status
		}
	}
	return ""
}

func five() []forge.Issue {
	return []forge.Issue{issue(1, ""), issue(2, ""), issue(3, ""), issue(4, ""), issue(5, "")}
}

func TestStartsUpToWorkers(t *testing.T) {
	e := newEnv(t, Config{Workers: 3}, five()...)
	p := e.start()
	if len(p.Started) != 3 || p.Free != 0 || p.Total != 3 {
		t.Fatalf("%+v", p)
	}
	if e.status(1) != StatusWip || e.status(3) != StatusWip || e.status(4) != StatusTodo || e.status(5) != StatusTodo {
		t.Fatalf("%+v", e.o.Roadmap().Items)
	}
}

func TestFinishFreesSlotAndStartsNext(t *testing.T) {
	e := newEnv(t, Config{Workers: 3}, five()...)
	e.start()
	e.r.handle(1).finish("merged")
	if n := e.waitStart(); n != 4 {
		t.Fatalf("got %d", n)
	}
	e.waitNote("#1 merged")
	if e.r.maxInFlight != 3 {
		t.Fatalf("max in flight %d", e.r.maxInFlight)
	}
}

func TestNeverTwoRunsOnOneIssue(t *testing.T) {
	e := newEnv(t, Config{}, issue(1, ""))
	e.r.gate = make(chan struct{})
	e.r.entered = make(chan struct{}, 4)
	e.r.gateIssue = 1
	e.o.roadmap = Roadmap{Items: []Item{{Issue: 1, Status: StatusTodo}}}
	ctx := context.Background()
	fin := make(chan finishedRun, 1)
	e.o.stop = make(chan struct{})
	first := make(chan struct{})
	go func() { e.o.fill(ctx, fin); close(first) }()
	<-e.r.entered // the first fill is now inside Start
	second := make(chan struct{})
	go func() { e.o.fill(ctx, fin); close(second) }()
	close(e.r.gate)
	<-first
	<-second
	if n := e.r.starts(1); n != 1 {
		t.Fatalf("Start called %d times", n)
	}
}

func TestFailedRunDoesNotStopOthers(t *testing.T) {
	e := newEnv(t, Config{Workers: 2}, five()[:3]...)
	e.start()
	e.r.handle(1).finish("failed")
	if n := e.waitStart(); n != 3 {
		t.Fatalf("got %d", n)
	}
	e.waitNote("#1 failed")
	if e.status(1) != StatusFailed || e.status(2) != StatusWip {
		t.Fatalf("%+v", e.o.Roadmap().Items)
	}
}

func TestDependencyOrder(t *testing.T) {
	e := newEnv(t, Config{}, issue(1, ""), issue(2, "depends on #1"))
	p := e.start()
	if len(p.Started) != 1 || p.Started[0] != 1 {
		t.Fatalf("%+v", p)
	}
	e.r.handle(1).finish("merged")
	if n := e.waitStart(); n != 2 {
		t.Fatalf("got %d", n)
	}
}

func TestFailedDependencyBlocksDependents(t *testing.T) {
	e := newEnv(t, Config{}, issue(1, ""), issue(2, "depends on #1"), issue(3, "depends on #2"))
	e.start()
	e.r.handle(1).finish("failed")
	e.waitNote("#2 blocked")
	e.waitNote("#3 blocked")
	if e.status(2) != StatusBlocked || e.status(3) != StatusBlocked || e.r.starts(2) != 0 {
		t.Fatalf("%+v", e.o.Roadmap().Items)
	}
}

func TestRunnerStartErrorMarksFailed(t *testing.T) {
	e := newEnv(t, Config{Workers: 1}, issue(1, ""), issue(2, ""))
	e.r.startErr[1] = errors.New("boom")
	e.start()
	if e.status(1) != StatusFailed || e.status(2) != StatusWip {
		t.Fatalf("%+v", e.o.Roadmap().Items)
	}
	e.o.mu.Lock()
	defer e.o.mu.Unlock()
	if e.o.inFlight[1] || !e.o.inFlight[2] {
		t.Fatalf("inFlight %v", e.o.inFlight)
	}
}

func TestAskKeepsSlot(t *testing.T) {
	e := newEnv(t, Config{Workers: 1}, issue(1, ""), issue(2, ""))
	e.start()
	e.r.handle(1).ask()
	e.waitNote("#1 waits for your answer")
	if e.status(1) != StatusAsk || e.status(2) != StatusTodo || e.r.starts(2) != 0 {
		t.Fatalf("%+v", e.o.Roadmap().Items)
	}
}

func TestAskThenMerged(t *testing.T) {
	e := newEnv(t, Config{Workers: 1}, issue(1, ""), issue(2, ""))
	e.start()
	e.r.handle(1).ask()
	e.waitNote("#1 waits")
	e.r.handle(1).finish("merged")
	if n := e.waitStart(); n != 2 {
		t.Fatalf("got %d", n)
	}
	if e.status(1) != StatusDone {
		t.Fatalf("%+v", e.o.Roadmap().Items)
	}
}

func TestNothingLeftFalseWhileAsk(t *testing.T) {
	o := New(Config{}, nil, nil, Hooks{})
	o.roadmap = Roadmap{Items: []Item{{Issue: 1, Status: StatusAsk}}}
	if o.nothingLeft() {
		t.Fatal("drained while a run asks")
	}
	o.roadmap.Items[0].Status = StatusDone
	if !o.nothingLeft() {
		t.Fatal("not drained")
	}
}

func TestDrainReleaseNeeded(t *testing.T) {
	e := newEnv(t, Config{}, issue(1, ""))
	e.start()
	e.f.Ms[0] = forge.Milestone{Number: 1, Title: "v0.4.0", ClosedCount: 1}
	e.r.handle(1).finish("merged")
	if p := e.waitReady(); !errors.Is(p.Special, forge.ErrReleaseNeeded) {
		t.Fatalf("%+v", p)
	}
	e.waitStop()
}

func TestDrainPicksNewIssuesOnce(t *testing.T) {
	e := newEnv(t, Config{}, issue(1, ""))
	e.start()
	e.f.Is = append(e.f.Is, issue(2, ""))
	e.f.Ms[0].OpenCount = 2
	e.r.handle(1).finish("merged")
	p := e.waitReady()
	if len(p.Started) != 1 || p.Started[0] != 2 || len(p.Roadmap.Items) != 2 || p.Roadmap.Counts.MilestoneAccepted != 2 {
		t.Fatalf("%+v", p)
	}
	e.f.Is = append(e.f.Is, issue(3, ""))
	e.r.handle(2).finish("merged")
	e.waitStop()
	if e.r.starts(3) != 0 || e.status(1) != StatusDone || e.status(2) != StatusDone {
		t.Fatalf("%+v", e.o.Roadmap().Items)
	}
}

func TestOracleFailureUsesFallback(t *testing.T) {
	e := newEnv(t, Config{Workers: 2}, five()[:3]...)
	p := e.start()
	if p.Roadmap.FromOracle || p.Roadmap.FallbackWhy == "" || len(p.Started) != 2 {
		t.Fatalf("%+v", p)
	}
}

func TestOracleAnswerIsUsed(t *testing.T) {
	e := newEnv(t, Config{Workers: 1}, issue(1, ""), issue(2, ""))
	e.r.oracle = func(string) RunResult {
		return RunResult{Outcome: "ended", Output: `{"order":[{"issue":2},{"issue":1}]}`}
	}
	p := e.start()
	if !p.Roadmap.FromOracle || len(p.Started) != 1 || p.Started[0] != 2 {
		t.Fatalf("%+v", p)
	}
}

func TestFirstFillNoPerItemNotices(t *testing.T) {
	e := newEnv(t, Config{Workers: 2}, five()[:3]...)
	p := e.start()
	if len(p.Started) != 2 {
		t.Fatalf("%+v", p)
	}
	for len(e.notes) > 0 {
		if l := <-e.notes; strings.Contains(l, "started #") {
			t.Fatalf("unexpected notice %q", l)
		}
	}
	e.r.handle(1).finish("merged")
	e.waitNote("started #3")
}

func TestForgeErrorAtStart(t *testing.T) {
	e := newEnv(t, Config{}, issue(1, ""))
	e.f.Err = errors.New("gh down")
	begin := time.Now()
	p := e.start()
	if p.Special == nil || len(e.r.started) != 0 || time.Since(begin) > wait {
		t.Fatalf("%+v", p)
	}
	e.waitStop()
}

func TestWorkersZeroInConfigIsUnlimited(t *testing.T) {
	e := newEnv(t, Config{}, five()...)
	p := e.start()
	if len(p.Started) != 5 || p.Free != -1 || p.Total != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestNewAppliesLabelAndWorkflowDefaults(t *testing.T) {
	o := New(Config{}, nil, nil, Hooks{})
	if o.cfg.Workflow != "issue-to-merge" || o.cfg.AcceptedLabel != "accepted" || o.cfg.Workers != 0 {
		t.Fatalf("%+v", o.cfg)
	}
}

func TestContextCancelStopsLoop(t *testing.T) {
	e := newEnv(t, Config{}, issue(1, ""))
	e.start()
	e.cancel()
	e.waitStop()
}

func TestPanicIsRecovered(t *testing.T) {
	e := newEnv(t, Config{}, issue(1, ""))
	e.o.h.PlanReady = func(PlanReady) { panic("kaboom") }
	e.o.Start(context.Background())
	e.waitNote("panic: kaboom")
	e.waitStop()
}
