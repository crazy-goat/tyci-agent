package flow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type externalHumanWorker struct {
	entered chan struct{}
	release <-chan struct{}
}

func (w *externalHumanWorker) Run(ctx context.Context, role, _ string, rc RunContext) (string, string, error) {
	if rc.StateName == "code" {
		close(w.entered)
		select {
		case <-w.release:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	return "done", "external-human-" + role, nil
}

type externalHumanChecks struct {
	checks  *fakeChecks
	gate    string
	entered chan struct{}
	release <-chan struct{}
}

func (c *externalHumanChecks) Run(ctx context.Context, s State, env []string, dir string) (string, CheckResult, error) {
	if s.Check == c.gate {
		close(c.entered)
		select {
		case <-c.release:
		case <-ctx.Done():
			return "", CheckResult{}, ctx.Err()
		}
	}
	return c.checks.Run(ctx, s, env, dir)
}

func waitExternalHumanStatus(t *testing.T, events <-chan RunEvent, status string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-events:
			if ev.Status == status {
				return
			}
			if ev.Status == "failed" {
				t.Fatalf("external human run failed: %+v", ev)
			}
		case <-timer.C:
			t.Fatalf("external human run did not reach %s", status)
		}
	}
}

func TestHumanReviewPoolKeepsExternalFixAndAcceptReservations(t *testing.T) {
	for _, action := range []string{"fix", "accept"} {
		t.Run(action, func(t *testing.T) {
			home, projectA, projectB := t.TempDir(), t.TempDir(), t.TempDir()
			wfA := semiRecoveryBuiltin(t, home, projectA)
			_ = semiRecoveryBuiltin(t, home, projectB)
			infoA := RepoInfo{Home: home, Root: projectA, Repo: "owner/a", Trusted: true}
			infoB := RepoInfo{Home: home, Root: projectB, Repo: "owner/b", Trusted: true}
			store := &Store{Dir: RunDir(home, infoA.Key(), "1")}
			st := newRun("code")
			st.Run, st.Workflow, st.Repo, st.Worktree = "1", "issue-to-merge", infoA.Repo, t.TempDir()
			initial := &semiRecoveryAgents{steps: []semiRecoveryAgentStep{
				{state: "code", role: "worker", key: "done"},
				{state: "review", role: "review", key: "ACCEPT"},
			}}
			if err := (&Runner{WF: wfA, Agents: initial, Store: store}).Run(context.Background(), st); !errors.Is(err, ErrPaused) {
				t.Fatal(err)
			}
			managerA := NewManager(nil, nil)
			managerA.Info = func() (RepoInfo, error) { return infoA, nil }
			managerB := NewManager(nil, nil)
			managerB.Info = func() (RepoInfo, error) { return infoB, nil }
			prepared := errors.New("repo B reached preparation")
			managerB.Prepare = func(context.Context, RepoInfo, StartRequest) (*RunState, *Workflow, []string, error) {
				return nil, nil, nil, prepared
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
				t.Fatalf("initial human reservation was missing: %v", err)
			}

			external := NewManager(nil, nil)
			external.Info = func() (RepoInfo, error) { return infoA, nil }
			ctx, cancel := context.WithCancel(context.Background())
			external.SetBase(ctx)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(func() { unblock(); cancel(); external.Shutdown(2 * time.Second) })
			external.NewRunner = func(_ RepoInfo, wf *Workflow, _ *RunState) *Runner {
				checks := &externalHumanChecks{checks: &fakeChecks{keys: map[string][]string{
					"checks/lock.sh": {"ok"}, "checks/rebase.sh": {"ok"}, "checks/post_review.sh": {"skip"},
					"checks/ci_wait.sh": {"green"}, "checks/fetch_comments.sh": {"none"}, "checks/merge.sh": {"merged"},
				}}, entered: entered, release: release}
				if action == "accept" {
					checks.gate = "checks/lock.sh"
				}
				return &Runner{WF: wf, Agents: &externalHumanWorker{entered: entered, release: release}, Checks: checks, Store: store}
			}
			events := make(chan RunEvent, 8)
			unsub := external.Subscribe(func(ev RunEvent) { events <- ev })
			defer unsub()
			if err := external.Resume("1", action); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("external worker/check did not start")
			}
			if err := startB(); !errors.Is(err, ErrNoSlot) {
				t.Errorf("pool released the external %s reservation while its worker/check was blocked: %v", action, err)
			}
			unblock()
			if action == "fix" {
				waitExternalHumanStatus(t, events, "paused")
				after, err := Load(store.Dir)
				if err != nil || after.Current != "human_review" || !isHumanReviewPause(after) {
					t.Fatalf("external fix did not return to its human gate: state=%+v error=%v", after, err)
				}
				if err := startB(); !errors.Is(err, ErrNoSlot) {
					t.Errorf("completed external fix lost its renewed human wait: %v", err)
				}
				if err := external.Resume("1", "accept"); err != nil {
					t.Fatal(err)
				}
			}
			waitExternalHumanStatus(t, events, "done")
			if err := startB(); !errors.Is(err, prepared) {
				t.Fatalf("completed external run kept its reservation: %v", err)
			}
		})
	}
}
