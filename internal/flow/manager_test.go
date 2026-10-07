package flow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gatedChecks blocks every check until release is closed, then returns key.
type gatedChecks struct {
	release chan struct{}
	key     string
	pr      string // written to <RunDir>/pr when set
	runDir  func() string
	panics  bool
}

func (g *gatedChecks) Run(ctx context.Context, _ State, _ []string, _ string) (string, CheckResult, error) {
	if g.panics {
		panic("boom")
	}
	if g.release != nil {
		select {
		case <-g.release:
		case <-ctx.Done():
			return "", CheckResult{}, ctx.Err()
		}
	}
	if g.pr != "" {
		_ = os.WriteFile(filepath.Join(g.runDir(), "pr"), []byte(g.pr), 0o600)
	}
	return g.key, CheckResult{}, nil
}

func demoWF() *Workflow {
	return &Workflow{Name: "demo", Start: "c", States: map[string]State{
		"c":   {Check: "c.sh", On: map[string]string{"ok": "end", "default": "ask"}},
		"ask": {Ask: "Need a decision.", On: map[string]string{"retry": "c", "stop": "end"}},
		"end": {End: true},
	}}
}

type mgrEnv struct {
	m       *Manager
	home    string
	notices chan string
	prepErr error
}

func newMgrEnv(t *testing.T, checks *gatedChecks) *mgrEnv {
	t.Helper()
	e := &mgrEnv{home: t.TempDir(), notices: make(chan string, 8)}
	info := RepoInfo{Home: e.home, Root: e.home, Repo: "o/r", DefaultBranch: "main"}
	runDir := func(id string) string { return RunDir(e.home, "r", id) }
	e.m = &Manager{
		Info:   func() (RepoInfo, error) { return info, nil },
		Notify: func(s string) { e.notices <- s },
		Prepare: func(_ context.Context, _ RepoInfo, req StartRequest) (*RunState, *Workflow, []string, error) {
			if e.prepErr != nil {
				return nil, nil, nil, e.prepErr
			}
			id := NewRunID(req.Issue, time.Now())
			st := &RunState{Version: 1, Run: id, Workflow: "demo", Repo: "o/r", Issue: req.Issue,
				Status: "running", Current: "c", Visits: map[string]int{}, History: []Step{}}
			if err := (&Store{Dir: runDir(id)}).Save(st); err != nil {
				return nil, nil, nil, err
			}
			return st, demoWF(), []string{"w1"}, nil
		},
		Workflow: func(RepoInfo, string) (*Workflow, error) { return demoWF(), nil },
		NewRunner: func(_ RepoInfo, wf *Workflow, st *RunState) *Runner {
			checks.runDir = func() string { return runDir(st.Run) }
			return &Runner{WF: wf, Checks: checks, Store: &Store{Dir: runDir(st.Run)}, RunDir: runDir(st.Run)}
		},
	}
	return e
}

func (e *mgrEnv) notice(t *testing.T) string {
	t.Helper()
	select {
	case s := <-e.notices:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no notice")
		return ""
	}
}

func TestWorkflowStart_AllowsSeveralActiveRuns(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	id1, _, err := e.m.Start(context.Background(), StartRequest{Issue: 1})
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := e.m.Start(context.Background(), StartRequest{Issue: 2})
	if err != nil || id1 == id2 {
		t.Fatalf("id2 = %q, err = %v", id2, err)
	}
	_, _, err = e.m.Start(context.Background(), StartRequest{Issue: 2})
	if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "issue 2") {
		t.Fatalf("same issue: err = %v", err)
	}
	close(c.release)
	e.notice(t)
	e.notice(t)
}

func TestManager_SubscribeGetsEndAndResume(t *testing.T) {
	c := &gatedChecks{key: "default"}
	e := newMgrEnv(t, c)
	events := make(chan RunEvent, 8)
	unsub := e.m.Subscribe(func(ev RunEvent) { events <- ev })
	defer unsub()
	id, _, err := e.m.Start(context.Background(), StartRequest{Issue: 3})
	if err != nil {
		t.Fatal(err)
	}
	get := func() RunEvent {
		select {
		case ev := <-events:
			return ev
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
			return RunEvent{}
		}
	}
	if ev := get(); ev.Run != id || ev.Status != "paused" {
		t.Fatalf("ev = %+v", ev)
	}
	if err := e.m.Resume(id, "stop"); err != nil {
		t.Fatal(err)
	}
	if ev := get(); ev.Status != "running" {
		t.Fatalf("ev = %+v", ev)
	}
	if ev := get(); ev.Status != "done" {
		t.Fatalf("ev = %+v", ev)
	}
}

func TestManager_RunText(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	if _, err := e.m.RunText(context.Background(), "roadmap", "{}"); err == nil {
		t.Fatal("want error without Text")
	}
	e.m.Text = func(_ context.Context, _ RepoInfo, wf *Workflow, in string) (string, error) {
		return wf.Name + ":" + in, nil
	}
	out, err := e.m.RunText(context.Background(), "roadmap", "{}")
	if err != nil || out != "demo:{}" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestWorkflowStart_RefusesSecondRunForSameIssue(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	old := &RunState{Version: 1, Run: "20260101-000000-5", Issue: 5, Status: "paused", Visits: map[string]int{}}
	if err := (&Store{Dir: RunDir(e.home, "r", old.Run)}).Save(old); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.m.Start(context.Background(), StartRequest{Issue: 5})
	if err == nil || !strings.Contains(err.Error(), "already has run "+old.Run) {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkflowStart_StaleRunningDoesNotBlock(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	old := &RunState{Version: 1, Run: "20260101-000000-5", Issue: 5, Status: "running", Visits: map[string]int{}}
	if err := (&Store{Dir: RunDir(e.home, "r", old.Run)}).Save(old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.m.Start(context.Background(), StartRequest{Issue: 5}); err != nil {
		t.Fatalf("err = %v", err)
	}
	e.notice(t)
}

func TestWorkflowStart_ReturnsImmediately(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	t0 := time.Now()
	_, warnings, err := e.m.Start(context.Background(), StartRequest{Issue: 1})
	if err != nil || time.Since(t0) > time.Second {
		t.Fatalf("err = %v, took %v", err, time.Since(t0))
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v", warnings)
	}
	close(c.release)
	e.notice(t)
}

func TestWorkflowStart_InvalidWorkflowCreatesNothing(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	e.prepErr = errors.New("workflow \"x\" is invalid")
	if _, _, err := e.m.Start(context.Background(), StartRequest{Issue: 1}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(filepath.Join(e.home, ".tyci")); !os.IsNotExist(err) {
		t.Fatalf(".tyci created: %v", err)
	}
	if len(e.m.active) != 0 {
		t.Fatal("run is active")
	}
}

func TestFinishedRun_PushesNotice(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok", pr: "171"})
	id, _, err := e.m.Start(context.Background(), StartRequest{Issue: 160})
	if err != nil {
		t.Fatal(err)
	}
	want := "workflow run " + id + " done: merged https://github.com/o/r/pull/171"
	if got := e.notice(t); got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}

func TestPausedRun_PushesNotice(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "bad"})
	id, _, _ := e.m.Start(context.Background(), StartRequest{Issue: 1})
	want := "workflow run " + id + " paused: Need a decision. (answer with workflow_resume: retry|stop)"
	if got := e.notice(t); got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}

func TestRunPanic_MarksFailedAndNotifies(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{panics: true})
	id, _, _ := e.m.Start(context.Background(), StartRequest{Issue: 1})
	got := e.notice(t)
	if !strings.Contains(got, id+" failed: panic") {
		t.Fatalf("notice = %q", got)
	}
	st, err := e.m.Status(id)
	if err != nil || st.Status != "failed" {
		t.Fatalf("status = %v, %v", st, err)
	}
}

func TestWorkflowStatus_ReportsCurrentState(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "bad"})
	id, _, _ := e.m.Start(context.Background(), StartRequest{Issue: 1})
	e.notice(t)
	for _, run := range []string{"", id} {
		st, err := e.m.Status(run)
		if err != nil || st.Run != id || st.Status != "paused" || st.Current != "ask" {
			t.Fatalf("run %q: %+v, %v", run, st, err)
		}
	}
	if _, err := e.m.Status("../x"); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestWorkflowResume_PassesAnswer(t *testing.T) {
	c := &gatedChecks{key: "bad"}
	e := newMgrEnv(t, c)
	id, _, _ := e.m.Start(context.Background(), StartRequest{Issue: 1})
	e.notice(t)
	waitIdle(t, e.m)
	if err := e.m.Resume(id, "stop"); err != nil {
		t.Fatal(err)
	}
	if got := e.notice(t); !strings.Contains(got, id+" done") {
		t.Fatalf("notice = %q", got)
	}
}

func TestWorkflowResume_BadAnswerListsKeys(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "bad"})
	id, _, _ := e.m.Start(context.Background(), StartRequest{Issue: 1})
	e.notice(t)
	waitIdle(t, e.m)
	err := e.m.Resume(id, "maybe")
	if err == nil || !strings.Contains(err.Error(), "retry, stop") {
		t.Fatalf("err = %v", err)
	}
	if st, _ := e.m.Status(id); st.Status != "paused" {
		t.Fatalf("status = %s", st.Status)
	}
}

func waitIdle(t *testing.T, m *Manager) {
	t.Helper()
	for i := 0; i < 200; i++ {
		m.mu.Lock()
		n := len(m.active)
		m.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run still active")
}

func TestShutdown_CancelsRun(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	id, _, _ := e.m.Start(context.Background(), StartRequest{Issue: 1})
	e.m.Shutdown(5 * time.Second)
	st, _ := e.m.Status(id)
	if st.Status != "failed" || st.Reason != "cancelled" {
		t.Fatalf("state = %s/%s", st.Status, st.Reason)
	}
}

func TestParseRemote(t *testing.T) {
	for url, want := range map[string]string{
		"git@github.com:crazy-goat/tyci-agent.git":         "crazy-goat/tyci-agent",
		"https://github.com/crazy-goat/tyci-agent.git":     "crazy-goat/tyci-agent",
		"https://github.com/crazy-goat/tyci-agent":         "crazy-goat/tyci-agent",
		"ssh://git@github.com/crazy-goat/tyci-agent.git\n": "crazy-goat/tyci-agent",
	} {
		if got, err := ParseRemote(url); err != nil || got != want {
			t.Errorf("ParseRemote(%q) = %q, %v", url, got, err)
		}
	}
	if _, err := ParseRemote("https://example.com/a/b.git"); err == nil {
		t.Error("non-GitHub remote accepted")
	}
}

func TestSkipHook_RemovesWorktreeKeepsRunDir(t *testing.T) {
	tmp := t.TempDir()
	sh := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	sh(repo, "init", "-q", "-b", "main")
	sh(repo, "commit", "-q", "--allow-empty", "-m", "init")
	sh(repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	wt := filepath.Join(tmp, "wt")
	sh(repo, "worktree", "add", "-q", "-b", "issue-9", wt, "main")
	runDir := filepath.Join(tmp, "run")
	if err := (&Store{Dir: runDir}).Save(&RunState{Version: 1, Run: "r", Visits: map[string]int{}}); err != nil {
		t.Fatal(err)
	}
	removeWorktreeHook(repo)(&RunState{Worktree: wt, Branch: "issue-9"})
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "state.json")); err != nil {
		t.Fatalf("run dir lost: %v", err)
	}
}
