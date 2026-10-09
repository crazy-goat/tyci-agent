package flow

import (
	"context"
	"os"
	"strings"
	"testing"
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
	gate := make(chan struct{})
	defer close(gate)
	e := newMgrEnv(t, &gatedChecks{release: gate, key: "ok"})
	if _, err := e.m.Stop("20990101-000000-1", ""); err == nil || !strings.Contains(err.Error(), "no active runs") {
		t.Fatalf("no active runs: err = %v", err)
	}
	id, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo", Params: []string{"4"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.m.Stop("20990101-000000-1", "")
	if err == nil || !strings.Contains(err.Error(), "is not active") || !strings.Contains(err.Error(), id) {
		t.Fatalf("unknown run: err = %v, want the active run %s", err, id)
	}
	if strings.Contains(err.Error(), "preparing:") {
		t.Fatalf("error leaks a reservation key: %v", err)
	}
}
