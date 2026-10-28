//go:build unix

package daemon

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flow"
)

// keyChecks answers every check with key.
type keyChecks struct{ key string }

func (k keyChecks) Run(context.Context, flow.State, []string, string) (string, flow.CheckResult, error) {
	return k.key, flow.CheckResult{}, nil
}

func demoWorkflow() *flow.Workflow {
	return &flow.Workflow{Name: "demo", Start: "c", Params: []flow.Param{{Name: "issue", Description: "issue", Required: true}}, States: map[string]flow.State{
		"c":   {Check: "c.sh", On: map[string]string{"ok": "end", "default": "ask"}},
		"ask": {Ask: "Need a decision.", On: map[string]string{"retry": "c", "stop": "end"}},
		"end": {End: true},
	}}
}

func newManager(t *testing.T, home, key string) *flow.Manager {
	t.Helper()
	info := flow.RepoInfo{Home: home, Root: home, Repo: repoName, DefaultBranch: "main"}
	n := 0
	return &flow.Manager{
		Info:     func() (flow.RepoInfo, error) { return info, nil },
		Workflow: func(flow.RepoInfo, string) (*flow.Workflow, error) { return demoWorkflow(), nil },
		Prepare: func(_ context.Context, _ flow.RepoInfo, req flow.StartRequest) (*flow.RunState, *flow.Workflow, []string, error) {
			n++
			issue, _ := strconv.Atoi(req.Params[0])
			st := &flow.RunState{Version: 1, Run: strconv.Itoa(n), Workflow: req.Workflow, Repo: repoName, Issue: issue, Status: "running", Current: "c", Worktree: home, Visits: map[string]int{}}
			return st, demoWorkflow(), nil, (&flow.Store{Dir: flow.RunDir(home, "r", st.Run)}).Save(st)
		},
		NewRunner: func(_ flow.RepoInfo, wf *flow.Workflow, st *flow.RunState) *flow.Runner {
			dir := flow.RunDir(home, "r", st.Run)
			return &flow.Runner{WF: wf, Checks: keyChecks{key}, Store: &flow.Store{Dir: dir}, RunDir: dir}
		},
	}
}

func result(t *testing.T, ch <-chan Result) Result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
		return Result{}
	}
}

func TestManagerRunsStartGivesTheEndAndTheAsk(t *testing.T) {
	home := t.TempDir()
	r := NewManagerRuns(newManager(t, home, "ok"), home, repoName)
	id, done, err := r.Start(context.Background(), Trigger{Kind: KindIssue, Repo: repoName, Workflow: "demo", Issue: 5})
	if err != nil || id == "" {
		t.Fatalf("start: %q %v", id, err)
	}
	if res := result(t, done); res.Status != "done" {
		t.Fatalf("result %+v", res)
	}

	home = t.TempDir()
	r = NewManagerRuns(newManager(t, home, "bad"), home, repoName)
	_, done, err = r.Start(context.Background(), Trigger{Kind: KindIssue, Repo: repoName, Workflow: "demo", Issue: 6})
	if err != nil {
		t.Fatal(err)
	}
	if res := result(t, done); res.Status != "paused" || !strings.HasPrefix(res.Reason, "Need a decision.") {
		t.Fatalf("result %+v", res)
	}
}

func TestManagerRunsContinuesAnInterruptedRun(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip(err)
	}
	st := &flow.RunState{Version: 1, Run: "7", Workflow: "demo", Repo: repoName, Issue: 3, Status: "running", Current: "c", Worktree: home, Visits: map[string]int{}, PID: cmd.ProcessState.Pid()}
	if err := (&flow.Store{Dir: flow.RunDir(home, "r", "7")}).Save(st); err != nil {
		t.Fatal(err)
	}
	r := NewManagerRuns(newManager(t, home, "ok"), home, repoName)
	ts, err := r.Interrupted()
	if err != nil || len(ts) != 1 || ts[0].Kind != KindResume || ts[0].Run != "7" || ts[0].Issue != 3 {
		t.Fatalf("interrupted %+v %v", ts, err)
	}
	id, done, err := r.Start(context.Background(), ts[0])
	if err != nil || id != "7" {
		t.Fatalf("resume: %q %v", id, err)
	}
	if res := result(t, done); res.Status != "done" {
		t.Fatalf("result %+v", res)
	}
}
