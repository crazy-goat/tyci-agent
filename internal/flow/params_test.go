package flow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/worktree"
)

// paramWF declares an issue param and an optional branch param.
func paramWF() *Workflow {
	return &Workflow{Name: "demo", Start: "a", Params: []Param{
		{Name: "issue", Description: "GitHub issue number", Required: true},
		{Name: "branch", Description: "branch to merge into"},
	}}
}

func TestBindParams_Positional(t *testing.T) {
	got, err := BindParams(paramWF(), []string{"160", "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if got["issue"] != "160" || got["branch"] != "dev" {
		t.Fatalf("params = %v", got)
	}
	got, err = BindParams(paramWF(), []string{"160"})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got["branch"]; !ok || v != "" {
		t.Fatalf("optional param without a value: %q, present %v", v, ok)
	}
}

func TestBindParams_ExtraValuesAreError(t *testing.T) {
	_, err := BindParams(paramWF(), []string{"160", "dev", "more"})
	if err == nil || !strings.Contains(err.Error(), "takes 2 param(s), got 3 value(s)") {
		t.Fatalf("err = %v", err)
	}
}

func TestBindParams_MissingRequiredNamesParam(t *testing.T) {
	_, err := BindParams(paramWF(), nil)
	if err == nil || !strings.Contains(err.Error(), `"issue"`) || !strings.Contains(err.Error(), "GitHub issue number") {
		t.Fatalf("err = %v", err)
	}
}

func TestBindParams_IssueMustBePositive(t *testing.T) {
	for _, v := range []string{"abc", "0", "-3", "1.5"} {
		if _, err := BindParams(paramWF(), []string{v}); err == nil || !strings.Contains(err.Error(), "positive") {
			t.Errorf("issue %q: err = %v", v, err)
		}
	}
}

func TestBindParams_NoIssueParamIsNoIssue(t *testing.T) {
	wf := &Workflow{Name: "demo", Params: []Param{{Name: "branch", Description: "b", Required: true}}}
	got, err := BindParams(wf, []string{"dev"})
	if err != nil {
		t.Fatal(err)
	}
	if issueOf(got) != 0 {
		t.Fatalf("issue = %d, want 0", issueOf(got))
	}
}

func TestValidateStructure_ParamNames(t *testing.T) {
	wf := &Workflow{Description: "d", Name: "demo", Start: "end", States: map[string]State{"end": {End: true}},
		Params: []Param{{Name: "Issue"}, {Name: "branch"}, {Name: "branch"}}}
	errs := validateStructure(wf)
	if len(errs) != 2 {
		t.Fatalf("errs = %v, want two", errs)
	}
	if !strings.Contains(errs[0].Error(), `"Issue"`) || !strings.Contains(errs[1].Error(), "declared twice") {
		t.Fatalf("errs = %v", errs)
	}
}

func TestTaskTemplates_ParamsAccess(t *testing.T) {
	wf := t.TempDir()
	e2eWrite(t, filepath.Join(wf, "tasks", "t.md"), "B={{.Params.branch}} I={{.Params.issue}}")
	tt := TaskTemplates{Dir: wf}
	rc := RunContext{Params: map[string]string{"issue": "160", "branch": "dev"}}
	if out, err := tt.Render("t", rc); err != nil || out != "B=dev I=160" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	e2eWrite(t, filepath.Join(wf, "tasks", "u.md"), "{{.Params.nope}}")
	if _, err := tt.Render("u", rc); err == nil {
		t.Fatal("undeclared param in a template rendered")
	}
}

// preparedDeps returns PrepareDeps for wf that record the worktree requests in
// worktreeCalls. The runs of home are stored under home.
func preparedDeps(home string, wf *Workflow, worktreeCalls *[]string) PrepareDeps {
	return PrepareDeps{
		Lookup: func(string) (*Workflow, string, error) { return wf, "test", nil },
		Config: func() (*flowconfig.Config, error) { return okCfg(), nil },
		AddWorktree: func(_ context.Context, runID string, _ int) (*worktree.Worktree, error) {
			*worktreeCalls = append(*worktreeCalls, runID)
			return &worktree.Worktree{Dir: filepath.Join(home, runID), Branch: "run-" + runID}, nil
		},
		NewStore: func(runID string) (*Store, error) {
			return &Store{Dir: RunDir(home, "r", runID)}, nil
		},
	}
}

func TestPrepareRun_NoIssueNamesWorktreeAfterRun(t *testing.T) {
	wf := okWF()
	wf.Params = []Param{{Name: "branch", Description: "b"}}
	var calls []string
	st, _, _, err := PrepareRun(context.Background(), preparedDeps(t.TempDir(), wf, &calls), PrepareReq{Workflow: "demo", Repo: "o/r", Params: []string{"dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Issue != 0 || len(calls) != 1 || calls[0] != st.Run {
		t.Fatalf("issue = %d, worktree calls = %v, run = %s", st.Issue, calls, st.Run)
	}
	if !strings.HasSuffix(st.Run, "-0") || st.Params["branch"] != "dev" {
		t.Fatalf("run = %s, params = %v", st.Run, st.Params)
	}
	if !strings.HasPrefix(st.Branch, "run-") {
		t.Fatalf("branch = %s", st.Branch)
	}
}

func TestPrepareRun_MissingRequiredParamCreatesNothing(t *testing.T) {
	var calls []string
	_, _, _, err := PrepareRun(context.Background(), preparedDeps(t.TempDir(), okWF(), &calls), PrepareReq{Workflow: "demo"})
	if err == nil || !strings.Contains(err.Error(), `"issue"`) {
		t.Fatalf("err = %v", err)
	}
	if len(calls) != 0 {
		t.Fatal("worktree created for a run with a missing param")
	}
}

// The params are saved in state.json and survive a load and a save again, which
// is what resume and status read.
func TestPrepareRun_ParamsSurviveSaveAndLoad(t *testing.T) {
	home := t.TempDir()
	var calls []string
	st, _, _, err := PrepareRun(context.Background(), preparedDeps(home, okWF(), &calls), PrepareReq{Workflow: "demo", Repo: "o/r", Params: []string{"7"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := RunDir(home, "r", st.Run)
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Params["issue"] != "7" || loaded.Issue != 7 {
		t.Fatalf("loaded params = %v, issue = %d", loaded.Params, loaded.Issue)
	}
	if err := (&Store{Dir: dir}).Save(loaded); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Params["issue"] != "7" {
		t.Fatalf("params after save = %v", again.Params)
	}
	if _, err := os.Stat(StatePath(dir)); err != nil {
		t.Fatal(err)
	}
}

// A run without an issue param: Start binds the params and refuses a missing
// required value before it reserves anything.
func TestWorkflowStart_MissingParamReservesNothing(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	if _, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo"}); err == nil || !strings.Contains(err.Error(), `"issue"`) {
		t.Fatalf("err = %v", err)
	}
	if len(e.m.active) != 0 {
		t.Fatal("a run is reserved")
	}
}

// A workflow without an issue param: the run has no issue and is not refused by
// another run of the same workflow.
func TestWorkflowStart_NoIssueWorkflow(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	wf := demoWF()
	wf.Params = nil
	e.m.Workflow = func(RepoInfo, string) (*Workflow, error) { return wf, nil }
	e.m.Prepare = func(_ context.Context, _ RepoInfo, req StartRequest) (*RunState, *Workflow, []string, error) {
		if len(req.Params) != 0 {
			t.Errorf("params = %v", req.Params)
		}
		id := NewRunID(0, time.Now())
		st := &RunState{Version: 1, Run: id, Workflow: "demo", Repo: "o/r", Issue: 0,
			Status: "running", Current: "c", Visits: map[string]int{}, History: []Step{}}
		if err := (&Store{Dir: RunDir(e.home, "r", id)}).Save(st); err != nil {
			return nil, nil, nil, err
		}
		return st, wf, nil, nil
	}
	id, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	e.notice(t)
	st, err := Load(RunDir(e.home, "r", id))
	if err != nil {
		t.Fatal(err)
	}
	if st.Issue != 0 {
		t.Fatalf("issue = %d", st.Issue)
	}
}
