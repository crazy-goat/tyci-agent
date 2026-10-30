package flow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/tyciconfig"
)

type semiRecoveryAgentStep struct {
	state string
	role  string
	key   string
	err   error
}

type semiRecoveryAgents struct {
	steps  []semiRecoveryAgentStep
	calls  []string
	visits []int
	gotos  []string
}

func (a *semiRecoveryAgents) Run(_ context.Context, role, _ string, rc RunContext) (string, string, error) {
	a.calls = append(a.calls, rc.StateName+"/"+role)
	a.visits = append(a.visits, rc.Visit)
	a.gotos = append(a.gotos, rc.Goto)
	if len(a.steps) == 0 {
		return "", "", fmt.Errorf("unexpected agent call %s/%s", rc.StateName, role)
	}
	step := a.steps[0]
	a.steps = a.steps[1:]
	if step.state != rc.StateName || step.role != role {
		return "", "", fmt.Errorf("agent call %s/%s, want %s/%s", rc.StateName, role, step.state, step.role)
	}
	return step.key, "semi-recovery-" + rc.StateName, step.err
}

func semiRecoveryBuiltin(t *testing.T, home, project string) *Workflow {
	t.Helper()
	if _, err := Init("issue-to-merge", "", project); err != nil {
		t.Fatal(err)
	}
	wf, _, err := Lookup("issue-to-merge", home, project, true)
	if err != nil {
		t.Fatal(err)
	}
	wf.Mode = tyciconfig.ModeSemi
	return wf
}

// Persist the real failed-agent transition, then simulate process exit before
// the ordinary ask is answered. Restart uses Load and Continue without editing
// history, counters, or the pending human-fix request.
type semiRecoveryCrashAfterWorkerError struct {
	store *Store
	err   error
}

func (s semiRecoveryCrashAfterWorkerError) Save(st *RunState) error {
	if err := s.store.Save(st); err != nil {
		return err
	}
	if st.Current == "ask" && len(st.History) > 0 {
		last := st.History[len(st.History)-1]
		if last.State == "code" && last.Kind == "agent" && last.Key == "error" {
			return s.err
		}
	}
	return nil
}

func TestHumanFixRecoveryPreservesCumulativeCodeVisits(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	wf := semiRecoveryBuiltin(t, home, project)
	if wf.States["ask"].Human {
		t.Fatal("the regression needs the actual oracle-capable builtin ask")
	}
	store := &Store{Dir: filepath.Join(home, "human-fix-recovery")}
	agents := &semiRecoveryAgents{steps: []semiRecoveryAgentStep{
		{state: "code", role: "worker", key: "done"},
		{state: "review", role: "review", key: "ACCEPT"},
		{state: "code", role: "worker", err: errors.New("simulated failed human fix")},
		{state: "ask", role: "oracle", key: "retry\nRetry the outstanding human fix."},
		{state: "code", role: "worker", key: "done"},
	}}
	st := newRun("code")
	st.Workflow = "issue-to-merge"
	st.Worktree = t.TempDir()
	runner := &Runner{WF: wf, Agents: agents, Store: store}
	if err := runner.Run(context.Background(), st); !errors.Is(err, ErrPaused) || st.Current != "human_review" {
		t.Fatalf("initial review state = %+v, error = %v", st, err)
	}
	interrupted := errors.New("simulated process exit after failed worker transition save")
	runner.Store = semiRecoveryCrashAfterWorkerError{store: store, err: interrupted}
	if err := runner.Resume(context.Background(), st, "fix address the human request"); !errors.Is(err, interrupted) {
		t.Fatalf("failed-worker boundary = %v", err)
	}
	st, err := Load(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != "ask" || st.Visits["code"] != 2 {
		t.Fatalf("saved failed-fix state = %+v", st)
	}
	runner = &Runner{WF: wf, Agents: agents, Store: store}
	if err := runner.Continue(context.Background(), st); !errors.Is(err, ErrPaused) || st.Current != "human_review" {
		t.Fatalf("recovered human-review state = %+v, error = %v", st, err)
	}
	st, err = Load(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents.steps) != 0 {
		t.Fatalf("scripted calls left = %+v; actual calls = %v", agents.steps, agents.calls)
	}
	if st.Visits["code"] != 3 {
		t.Fatalf("human-fix recovery reset cumulative code visits: got %d, want 3 (initial + failed fix + retry); calls=%v visit contexts=%v history=%+v", st.Visits["code"], agents.calls, agents.visits, st.History)
	}
}

func TestHumanFixRecoveryRetryCannotExceedVisitBudget(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	wf := semiRecoveryBuiltin(t, home, project)
	agents := &semiRecoveryAgents{steps: []semiRecoveryAgentStep{
		{state: "code", role: "worker", key: "done"},
		{state: "review", role: "review", key: "ACCEPT"},
		{state: "code", role: "worker", err: errors.New("first human-fix failure")},
		{state: "ask", role: "oracle", key: "retry\nRetry the human fix."},
		{state: "code", role: "worker", err: errors.New("second human-fix failure")},
		{state: "ask", role: "oracle", key: "retry\nRetry the human fix again."},
	}}
	st := newRun("code")
	runner := &Runner{WF: wf, Agents: agents, Store: &Store{Dir: t.TempDir()}}
	if err := runner.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatal(err)
	}
	if err := runner.Resume(context.Background(), st, "fix the human request"); !errors.Is(err, ErrPaused) || st.Current != "human_review" || st.Visits["code"] != 3 {
		t.Fatalf("oracle retries exceeded the human-fix budget: state=%+v error=%v", st, err)
	}
	if len(agents.steps) != 0 || len(agents.calls) != 6 {
		t.Fatalf("unexpected recovery execution: calls=%v remaining=%+v", agents.calls, agents.steps)
	}
}

func semiRecoveryWaitDone(t *testing.T, events <-chan RunEvent) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-events:
			if ev.Status == "done" {
				return
			}
			if ev.Status == "failed" {
				t.Fatalf("external resume failed: %+v", ev)
			}
		case <-timer.C:
			t.Fatal("external resume did not finish")
		}
	}
}

func TestHumanReviewPoolObservesExternalAcceptance(t *testing.T) {
	home, projectA, projectB := t.TempDir(), t.TempDir(), t.TempDir()
	wfA := semiRecoveryBuiltin(t, home, projectA)
	_ = semiRecoveryBuiltin(t, home, projectB)
	infoA := RepoInfo{Home: home, Root: projectA, Repo: "owner/a", Trusted: true}
	infoB := RepoInfo{Home: home, Root: projectB, Repo: "owner/b", Trusted: true}
	dirA := RunDir(home, infoA.Key(), "1")
	storeA := &Store{Dir: dirA}
	initialAgents := &semiRecoveryAgents{steps: []semiRecoveryAgentStep{
		{state: "code", role: "worker", key: "done"},
		{state: "review", role: "review", key: "ACCEPT"},
	}}
	st := newRun("code")
	st.Run, st.Workflow, st.Repo, st.Worktree = "1", "issue-to-merge", infoA.Repo, t.TempDir()
	if err := (&Runner{WF: wfA, Agents: initialAgents, Store: storeA}).Run(context.Background(), st); !errors.Is(err, ErrPaused) || st.Current != "human_review" {
		t.Fatalf("initial persisted human gate = %+v, error = %v", st, err)
	}

	// The first process's already-open managers recover the saved reservation.
	managerA := NewManager(nil, nil)
	managerA.Info = func() (RepoInfo, error) { return infoA, nil }
	managerB := NewManager(nil, nil)
	managerB.Info = func() (RepoInfo, error) { return infoB, nil }
	prepareReached := errors.New("repo B reached preparation")
	managerB.Prepare = func(context.Context, RepoInfo, StartRequest) (*RunState, *Workflow, []string, error) {
		return nil, nil, nil, prepareReached
	}
	pool := &Pool{}
	pool.Join(managerA)
	pool.Join(managerB)
	managerB.SetWorkers(1)
	startB := func() error {
		_, _, err := managerB.Start(context.Background(), StartRequest{Workflow: "issue-to-merge", Params: []string{"2"}})
		return err
	}
	if err := startB(); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("precondition: repo A's human gate did not hold the pool slot: %v", err)
	}

	// Standalone workflow resume creates a fresh manager, as this third one
	// does. It uses the persisted state and the actual builtin acceptance path.
	external := NewManager(nil, nil)
	external.Info = func() (RepoInfo, error) { return infoA, nil }
	checks := &fakeChecks{keys: map[string][]string{
		"checks/lock.sh":           {"ok"},
		"checks/rebase.sh":         {"ok"},
		"checks/post_review.sh":    {"skip"},
		"checks/ci_wait.sh":        {"green"},
		"checks/fetch_comments.sh": {"none"},
		"checks/merge.sh":          {"merged"},
	}}
	finalAgents := &semiRecoveryAgents{steps: []semiRecoveryAgentStep{{state: "findings", role: "review", key: "done"}}}
	external.NewRunner = func(_ RepoInfo, wf *Workflow, _ *RunState) *Runner {
		return &Runner{WF: wf, Agents: finalAgents, Checks: checks, Store: storeA}
	}
	events := make(chan RunEvent, 8)
	unsub := external.Subscribe(func(ev RunEvent) { events <- ev })
	defer unsub()
	if err := external.Resume("1", "accept"); err != nil {
		t.Fatal(err)
	}
	semiRecoveryWaitDone(t, events)
	after, err := Load(dirA)
	if err != nil || after.Status != "done" {
		t.Fatalf("external saved result = %+v, error = %v", after, err)
	}
	// Do not mutate managerA or clear its cache: admission must observe the
	// supported external decision from disk without a local-run side effect.
	if err := startB(); !errors.Is(err, prepareReached) {
		t.Fatalf("pooled human reservation stayed occupied after external resume completed: got %v, want repository B to reach preparation", err)
	}
}

func TestHumanReviewPoolObservesExternalPauseCreatedAfterJoin(t *testing.T) {
	home, projectA, projectB := t.TempDir(), t.TempDir(), t.TempDir()
	wfA := semiRecoveryBuiltin(t, home, projectA)
	_ = semiRecoveryBuiltin(t, home, projectB)
	infoA := RepoInfo{Home: home, Root: projectA, Repo: "owner/a", Trusted: true}
	infoB := RepoInfo{Home: home, Root: projectB, Repo: "owner/b", Trusted: true}
	managerA := NewManager(nil, nil)
	managerA.Info = func() (RepoInfo, error) { return infoA, nil }
	managerB := NewManager(nil, nil)
	managerB.Info = func() (RepoInfo, error) { return infoB, nil }
	prepareReached := errors.New("repo B reached preparation")
	managerB.Prepare = func(context.Context, RepoInfo, StartRequest) (*RunState, *Workflow, []string, error) {
		return nil, nil, nil, prepareReached
	}
	pool := &Pool{}
	pool.Join(managerA)
	pool.Join(managerB)
	managerB.SetWorkers(1)
	startB := func() error {
		_, _, err := managerB.Start(context.Background(), StartRequest{Workflow: "issue-to-merge", Params: []string{"2"}})
		return err
	}
	if err := startB(); !errors.Is(err, prepareReached) || managerA.usedSlots().total != 0 {
		t.Fatalf("empty pool precondition failed: %v, usage=%+v", err, managerA.usedSlots())
	}

	// A standalone process creates a run after both pool members are already
	// open. Execute the real builtin graph to persist its human gate.
	st := newRun("code")
	st.Run, st.Workflow, st.Repo, st.Worktree = "1", "issue-to-merge", infoA.Repo, t.TempDir()
	storeA := &Store{Dir: RunDir(home, infoA.Key(), st.Run)}
	agents := &semiRecoveryAgents{steps: []semiRecoveryAgentStep{
		{state: "code", role: "worker", key: "done"},
		{state: "review", role: "review", key: "ACCEPT"},
	}}
	if err := (&Runner{WF: wfA, Agents: agents, Store: storeA}).Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatal(err)
	}
	if err := startB(); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("pool missed the externally created human reservation after an empty join: %v", err)
	}
}
