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

// workflowNamed is demoWorkflow; fix-ci has no issue param.
func workflowNamed(name string) *flow.Workflow {
	wf := demoWorkflow()
	if name == FixCIWorkflow {
		wf.Name, wf.Params = name, nil
	}
	return wf
}

func newManager(t *testing.T, home, key string) *flow.Manager {
	t.Helper()
	info := flow.RepoInfo{Home: home, Root: home, Repo: repoName, DefaultBranch: "main"}
	n := 0
	return &flow.Manager{
		Info:     func() (flow.RepoInfo, error) { return info, nil },
		Workflow: func(_ flow.RepoInfo, name string) (*flow.Workflow, error) { return workflowNamed(name), nil },
		Prepare: func(_ context.Context, _ flow.RepoInfo, req flow.StartRequest) (*flow.RunState, *flow.Workflow, []string, error) {
			n++
			issue := 0
			if len(req.Params) > 0 {
				issue, _ = strconv.Atoi(req.Params[0])
			}
			st := &flow.RunState{Version: 1, Run: strconv.Itoa(n), Workflow: req.Workflow, Repo: repoName, Issue: issue, Trigger: req.Trigger, Status: "running", Current: "c", Worktree: home, Visits: map[string]int{}}
			return st, workflowNamed(req.Workflow), nil, (&flow.Store{Dir: flow.RunDir(home, "r", st.Run)}).Save(st)
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

func TestManagerRunsFixCIRunKeepsTheTriggerKey(t *testing.T) {
	home := t.TempDir()
	r := NewManagerRuns(newManager(t, home, "ok"), home, repoName)
	key := "ci_failure:o/r:main@abc"
	id, done, err := r.Start(context.Background(), Trigger{Kind: KindCIFailure, Repo: repoName, Workflow: FixCIWorkflow, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	result(t, done)
	st, err := flow.Load(flow.RunDir(home, "r", id))
	if err != nil || st.Trigger != key {
		t.Fatalf("run file trigger %q, want %q (%v)", st.Trigger, key, err)
	}
}

func TestManagerRunsAskOfThisRepoWhenAnotherRepoHasTheSameRunNumber(t *testing.T) {
	home := t.TempDir()
	// Run #1 of another repository, paused with another ask.
	other := &flow.RunState{Version: 1, Run: "1", Workflow: "demo", Repo: "x/other", Status: "paused", Ask: &flow.Ask{Message: "other repo ask"}}
	if err := (&flow.Store{Dir: flow.RunDir(home, "other", "1")}).Save(other); err != nil {
		t.Fatal(err)
	}
	r := NewManagerRuns(newManager(t, home, "bad"), home, repoName)
	id, done, err := r.Start(context.Background(), Trigger{Kind: KindIssue, Repo: repoName, Workflow: "demo", Issue: 6})
	if err != nil || id != "1" {
		t.Fatalf("start %q %v", id, err)
	}
	res := result(t, done)
	st, err := flow.Load(flow.RunDir(home, "r", id))
	if err != nil || st.Ask == nil || res.Reason != st.Ask.Message {
		t.Fatalf("result %+v, want the ask of this repo's run file (%v)", res, err)
	}
}
