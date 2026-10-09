package flow

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// withState changes the state that Prepare creates, before the run starts.
func withState(e *mgrEnv, change func(*RunState)) {
	prepare := e.m.Prepare
	e.m.Prepare = func(ctx context.Context, info RepoInfo, req StartRequest) (*RunState, *Workflow, []string, error) {
		st, wf, warnings, err := prepare(ctx, info, req)
		if st != nil {
			change(st)
		}
		return st, wf, warnings, err
	}
}

func TestStop_ActiveRunEndsStopped(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{release: make(chan struct{}), key: "ok"})
	worktree := t.TempDir()
	withState(e, func(st *RunState) { st.Worktree = worktree })
	id, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo", Params: []string{"1"}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.m.Stop(id, "test reason")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if st.Status != StatusStopped || st.Reason != "test reason" {
		t.Fatalf("returned state = %s/%s", st.Status, st.Reason)
	}
	saved, err := e.m.Status(id)
	if err != nil || saved.Status != StatusStopped || saved.Reason != "test reason" {
		t.Fatalf("saved state = %+v, err = %v", saved, err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree must stay: %v", err)
	}
	if got := e.notice(t); !strings.Contains(got, "stopped: no PR") {
		t.Fatalf("notice = %q", got)
	}
}

// TestStop_KeepsPullRequest sets the PR in Prepare, not through gatedChecks.pr:
// the runner reads the pr file only after a check succeeds, and a stopped check
// never succeeds, so gatedChecks cannot write the PR of a run that is stopped.
func TestStop_KeepsPullRequest(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{release: make(chan struct{}), key: "ok"})
	withState(e, func(st *RunState) { st.PR = 12 })
	id, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo", Params: []string{"2"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Stop(id, ""); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	saved, err := e.m.Status(id)
	if err != nil || saved.PR != 12 {
		t.Fatalf("saved PR = %d, err = %v", saved.PR, err)
	}
	if got := e.notice(t); !strings.Contains(got, "stopped: PR https://github.com/o/r/pull/12 is still open") {
		t.Fatalf("notice = %q", got)
	}
}

func TestStop_DefaultReason(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{release: make(chan struct{}), key: "ok"})
	id, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo", Params: []string{"3"}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.m.Stop(id, "")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if st.Reason != "stopped by user" {
		t.Fatalf("reason = %q", st.Reason)
	}
}

func TestStop_UnknownRunListsActiveIds(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{release: make(chan struct{}), key: "ok"})
	if _, err := e.m.Stop("20990101-000000-1", ""); err == nil || !strings.Contains(err.Error(), "no active runs") {
		t.Fatalf("no active runs: err = %v", err)
	}
	id, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo", Params: []string{"4"}})
	if err != nil {
		t.Fatal(err)
	}
	// Runs until the test ends must end before the TempDir cleanup. Cleanups run last in first out.
	t.Cleanup(func() { _, _ = e.m.Stop(id, "") })
	_, err = e.m.Stop("20990101-000000-1", "")
	if err == nil || !strings.Contains(err.Error(), "is not active") || !strings.Contains(err.Error(), id) {
		t.Fatalf("unknown run: err = %v, want the active run %s", err, id)
	}
	if strings.Contains(err.Error(), "preparing:") {
		t.Fatalf("error leaks a reservation key: %v", err)
	}
}

func TestShutdown_StillFailsWithCancelled(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{release: make(chan struct{}), key: "ok"})
	id, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo", Params: []string{"5"}})
	if err != nil {
		t.Fatal(err)
	}
	e.m.Shutdown(5 * time.Second)
	st, err := e.m.Status(id)
	if err != nil || st.Status != "failed" || st.Reason != "cancelled" {
		t.Fatalf("state = %+v, err = %v", st, err)
	}
}

func TestNotice_Stopped_WithPR(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	e.m.notify(&RunState{Run: "r1", Status: StatusStopped, Repo: "o/r", PR: 12}, demoWF())
	if got := e.notice(t); got != "workflow run r1 stopped: PR https://github.com/o/r/pull/12 is still open" {
		t.Fatalf("notice = %q", got)
	}
}

func TestNotice_Stopped_NoPR(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	e.m.notify(&RunState{Run: "r2", Status: StatusStopped, Repo: "o/r"}, demoWF())
	if got := e.notice(t); got != "workflow run r2 stopped: no PR" {
		t.Fatalf("notice = %q", got)
	}
}

// savePausedRun writes a paused run of workflow demo at state ask.
func savePausedRun(t *testing.T, e *mgrEnv, id string) {
	t.Helper()
	st := &RunState{Version: 1, Run: id, Workflow: "demo", Repo: "o/r", Issue: 1,
		Status: "paused", Current: "ask", Visits: map[string]int{}, History: []Step{}}
	if err := (&Store{Dir: RunDir(e.home, "r", id)}).Save(st); err != nil {
		t.Fatal(err)
	}
}

func TestStop_PausedRunHintListsItsAnswers(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{release: make(chan struct{}), key: "ok"})
	savePausedRun(t, e, "20990101-000000-2")
	_, err := e.m.Stop("20990101-000000-2", "")
	if err == nil || !strings.Contains(err.Error(), "one of the answers: retry, stop") {
		t.Fatalf("err = %v, want the answers of the paused state", err)
	}
}

// A workflow without a "stop" key must not get a hint that names one.
func TestStop_PausedRunWithoutStopKeyHintHasNoStop(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{release: make(chan struct{}), key: "ok"})
	e.m.Workflow = func(RepoInfo, string) (*Workflow, error) {
		wf := demoWF()
		wf.States["ask"] = State{Ask: "Need a decision.", On: map[string]string{"retry": "c"}}
		return wf, nil
	}
	savePausedRun(t, e, "20990101-000000-3")
	_, err := e.m.Stop("20990101-000000-3", "")
	if err == nil || !strings.Contains(err.Error(), "one of the answers: retry") {
		t.Fatalf("err = %v, want the answer retry", err)
	}
	if strings.Contains(err.Error(), "stop") {
		t.Fatalf("hint names a stop answer the workflow does not have: %v", err)
	}
}
