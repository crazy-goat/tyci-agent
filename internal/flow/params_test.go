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
	if !strings.Contains(st.Run, "-0-") || st.Params["branch"] != "dev" {
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
	work, _ := newRepo(t)
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	wf := demoWF()
	wf.Params = nil
	e.m.Workflow = func(RepoInfo, string) (*Workflow, error) { return wf, nil }
	info := RepoInfo{Home: e.home, Root: work, Repo: "o/r", DefaultBranch: "main"}
	e.m.Prepare = func(ctx context.Context, _ RepoInfo, req StartRequest) (*RunState, *Workflow, []string, error) {
		if len(req.Params) != 0 {
			t.Errorf("params = %v", req.Params)
		}
		id := NewRunID(0, time.Now())
		wt, err := addRunWorktree(ctx, info, id, 0)
		if err != nil {
			return nil, nil, nil, err
		}
		st := &RunState{Version: 1, Run: id, Workflow: "demo", Repo: "o/r", Issue: 0,
			Branch: wt.Branch, Worktree: wt.Dir,
			Status: "running", Current: "c", Visits: map[string]int{}, History: []Step{}}
		if err := (&Store{Dir: RunDir(e.home, "r", id)}).Save(st); err != nil {
			return nil, nil, nil, err
		}
		return st, wf, nil, nil
	}
	// Both starts run in the same second: each must still get its own run id.
	id1, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	e.notice(t)
	e.notice(t)
	st1, err := Load(RunDir(e.home, "r", id1))
	if err != nil {
		t.Fatal(err)
	}
	st2, err := Load(RunDir(e.home, "r", id2))
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 || st1.Worktree == st2.Worktree {
		t.Fatalf("runs share an id or worktree: %s %s / %s %s", id1, st1.Worktree, id2, st2.Worktree)
	}
	if st1.Issue != 0 || st2.Issue != 0 {
		t.Fatalf("issue = %d, %d", st1.Issue, st2.Issue)
	}
}

// addRunWorktree names the worktree after the issue of a run that has one, and
// after the run id of a run that has none.
func TestAddRunWorktree_IssueOrRun(t *testing.T) {
	work, _ := newRepo(t)
	info := RepoInfo{Home: t.TempDir(), Root: work, Repo: "o/r", DefaultBranch: "main"}
	ctx := context.Background()
	withIssue, err := addRunWorktree(ctx, info, NewRunID(7, time.Now()), 7)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = withIssue.Remove(ctx) })
	if withIssue.Branch != "issue-7" || !strings.HasSuffix(withIssue.Dir, "issue-7") {
		t.Fatalf("issue run: branch %q dir %q", withIssue.Branch, withIssue.Dir)
	}
	runID := NewRunID(0, time.Now())
	noIssue, err := addRunWorktree(ctx, info, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = noIssue.Remove(ctx) })
	if noIssue.Branch != "run-"+runID || !strings.HasSuffix(noIssue.Dir, "run-"+runID) {
		t.Fatalf("no-issue run: branch %q dir %q", noIssue.Branch, noIssue.Dir)
	}
}

// IssueArgs puts the issue on the param named "issue", never on another param.
func TestIssueArgs_BindsByName(t *testing.T) {
	wf := demoWF()
	wf.Params = []Param{{Name: "branch", Description: "b"}, {Name: "issue", Description: "n", Required: true}}
	args, err := IssueArgs(wf, 160)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != "" || args[1] != "160" {
		t.Fatalf("args = %q", args)
	}
	params, err := BindParams(wf, args)
	if err != nil {
		t.Fatal(err)
	}
	if params["issue"] != "160" || params["branch"] != "" {
		t.Fatalf("params = %v", params)
	}
}

// A workflow without an issue param refuses an issue start before it reserves
// anything, and the error gives the way to add one.
func TestStartIssue_RefusesWorkflowWithoutIssueParam(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	e.m.Prepare = func(context.Context, RepoInfo, StartRequest) (*RunState, *Workflow, []string, error) {
		t.Error("Prepare was called")
		return nil, nil, nil, nil
	}
	wf := demoWF()
	wf.Params = []Param{{Name: "branch", Description: "b"}}
	e.m.Workflow = func(RepoInfo, string) (*Workflow, error) { return wf, nil }
	if _, _, err := e.m.StartIssue(context.Background(), "demo", 5); err == nil || !strings.Contains(err.Error(), `no "issue" param`) {
		t.Fatalf("err = %v", err)
	}
	wf.Params = nil
	if _, _, err := e.m.StartIssue(context.Background(), "demo", 5); err == nil || !strings.Contains(err.Error(), "workflow.json") {
		t.Fatalf("err = %v", err)
	}
}

// validate warns about a workflow that an issue cannot start.
func TestValidate_NoIssueParamWarns(t *testing.T) {
	wf := &Workflow{Description: "d", Name: "demo", Start: "a", States: map[string]State{"a": {End: true}}}
	warns, err := Validate(wf, &flowconfig.Config{}, okResolve)
	if err != nil || len(warns) != 1 || !strings.Contains(warns[0], "cannot start with an issue") {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
}

// The issue param holds the canonical integer text, the same as {{.Issue}}.
func TestBindParams_IssueIsCanonical(t *testing.T) {
	params, err := BindParams(demoWF(), []string{"+007"})
	if err != nil {
		t.Fatal(err)
	}
	if params["issue"] != "7" {
		t.Fatalf("issue param = %q, want 7", params["issue"])
	}
}

// Two run ids made in the same second differ, and an issue run keeps its id.
func TestNewRunID_NoIssueIsUnique(t *testing.T) {
	now := time.Date(2026, 10, 9, 10, 10, 10, 0, time.UTC)
	a, b := NewRunID(0, now), NewRunID(0, now)
	if a == b || !runIDPattern.MatchString(a) || !runIDPattern.MatchString(b) {
		t.Fatalf("ids %q and %q", a, b)
	}
	if got := NewRunID(160, now); got != "20261009-101010-160" {
		t.Fatalf("issue id = %q", got)
	}
}
