package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/forge/fake"
)

// gatedForge is fake.Fake that blocks the first milestone read until gate is
// closed, then fails with its Err. The orchestrator holds its adopted runs until
// the test says so.
type gatedForge struct {
	fake.Fake
	gate chan struct{}
}

func (g *gatedForge) Milestones(ctx context.Context) ([]forge.Milestone, error) {
	<-g.gate
	return g.Fake.Milestones(ctx)
}

// okChecks passes every check at once, so a resumed run ends right away.
type okChecks struct{}

func (okChecks) Run(context.Context, flow.State, []string, string) (string, flow.CheckResult, error) {
	return "ok", flow.CheckResult{}, nil
}

// deadPID returns the PID of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("true not runnable: ", err)
	}
	return cmd.ProcessState.Pid()
}

// saveRun writes a run of issue in repo o/r under home. A running run has a
// dead owner, so Start resumes it; a paused run waits for an answer.
func saveRun(t *testing.T, home string, issue int, status string) {
	t.Helper()
	st := &flow.RunState{
		Version: 1, Run: fmt.Sprintf("20260101-000000-%d", issue), Workflow: "demo", Repo: "o/r", Issue: issue,
		Status: status, Current: "c", Worktree: t.TempDir(),
		Visits: map[string]int{"c": 2}, History: []flow.Step{},
	}
	if status == "paused" {
		st.Current, st.Ask = "ask", &flow.Ask{Message: "Need a decision."}
	} else {
		st.PID = deadPID(t)
	}
	if err := (&flow.Store{Dir: flow.RunDir(home, "r", st.Run)}).Save(st); err != nil {
		t.Fatal(err)
	}
}

// demoWorkflow is a one-check workflow with the issue param.
func demoWorkflow() *flow.Workflow {
	return &flow.Workflow{Name: "demo", Start: "c", Params: []flow.Param{{Name: "issue", Description: "GitHub issue number", Required: true}}, States: map[string]flow.State{
		"c":   {Check: "c.sh", On: map[string]string{"ok": "end", "default": "ask"}},
		"ask": {Ask: "Need a decision.", On: map[string]string{"retry": "c", "stop": "end"}},
		"end": {End: true},
	}}
}

// TestStopReleasesHeldRunSlot runs the orchestrator with a real flow.Manager. It
// adopts a paused run that takes the only worker slot. A resume by hand is refused
// while the orchestrator holds the run, and passes after the orchestrator stops.
func TestStopReleasesHeldRunSlot(t *testing.T) {
	home := t.TempDir()
	saveRun(t, home, 5, "paused")
	saveRun(t, home, 6, "running")
	info := flow.RepoInfo{Home: home, Root: home, Repo: "o/r", DefaultBranch: "main"}
	notes := make(chan string, 8)
	m := &flow.Manager{
		Info:     func() (flow.RepoInfo, error) { return info, nil },
		Workflow: func(flow.RepoInfo, string) (*flow.Workflow, error) { return demoWorkflow(), nil },
		NewRunner: func(_ flow.RepoInfo, wf *flow.Workflow, st *flow.RunState) *flow.Runner {
			dir := flow.RunDir(home, "r", st.Run)
			return &flow.Runner{WF: wf, Checks: okChecks{}, Store: &flow.Store{Dir: dir}, RunDir: dir}
		},
		Notify: func(s string) { notes <- s },
	}
	m.SetWorkers(1)
	gate := make(chan struct{})
	o := New(Config{Workers: 1}, &gatedForge{Fake: fake.Fake{Err: errors.New("boom")}, gate: gate}, NewRunner(m), Hooks{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.Start(ctx)
	resume := func() error {
		_, _, err := m.Start(ctx, flow.StartRequest{Workflow: "demo", Params: []string{"6"}})
		return err
	}
	if err := resume(); !errors.Is(err, flow.ErrNoSlot) {
		t.Fatalf("resume while the orchestrator holds run 5: err = %v", err)
	}
	close(gate)
	select {
	case <-o.stop:
	case <-time.After(wait):
		t.Fatal("orchestrator did not stop")
	}
	if err := resume(); err != nil {
		t.Fatalf("resume after the orchestrator stopped: %v", err)
	}
	select {
	case <-notes:
	case <-time.After(wait):
		t.Fatal("resumed run did not end")
	}
}
