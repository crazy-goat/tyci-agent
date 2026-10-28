//go:build unix

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/cron"
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/forge/fake"
)

const repoName = "o/r"

// fakeRuns writes a run file for every start, like flow.Manager does, so the
// file dedup of the pollers sees it.
type fakeRuns struct {
	home string
	// result is the end of every run. Default: done.
	result Result
	// hold keeps the runs running until it is closed. Nil: runs end at once.
	hold chan struct{}

	mu               sync.Mutex
	started          []Trigger
	running, maxRuns int
	startErr         error
	interrupted      []Trigger
}

func (f *fakeRuns) Start(_ context.Context, t Trigger) (string, <-chan Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", nil, f.startErr
	}
	id := t.Run
	if t.Kind != KindResume {
		var err error
		if id, err = flow.NewRunID(f.home, "r"); err != nil {
			return "", nil, err
		}
	}
	st := &flow.RunState{Version: 1, Run: id, Workflow: t.Workflow, Repo: t.Repo, Issue: t.Issue, Status: "running", StartedAt: time.Now()}
	store := flow.Store{Dir: flow.RunDir(f.home, "r", id)}
	if err := store.Save(st); err != nil {
		return "", nil, err
	}
	f.started = append(f.started, t)
	f.running++
	f.maxRuns = max(f.maxRuns, f.running)
	res := f.result
	if res.Status == "" {
		res.Status = "done"
	}
	hold := f.hold
	ch := make(chan Result, 1)
	go func() {
		if hold != nil {
			<-hold
		}
		f.mu.Lock()
		st.Status = res.Status
		_ = store.Save(st)
		f.running--
		f.mu.Unlock()
		ch <- res
	}()
	return id, ch, nil
}

func (f *fakeRuns) Interrupted() ([]Trigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.interrupted, nil
}

// release ends the held runs and waits until their files are written.
func (f *fakeRuns) release(t *testing.T) {
	close(f.hold)
	waitFor(t, "runs end", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.running == 0
	})
}

func (f *fakeRuns) starts() []Trigger {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Trigger(nil), f.started...)
}

// countForge counts the polls and can fail them. It is safe for concurrent use.
type countForge struct {
	*fake.Fake
	mu    sync.Mutex
	polls int
	err   error
}

func (c *countForge) Milestones(ctx context.Context) ([]forge.Milestone, error) {
	c.mu.Lock()
	c.polls++
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return c.Fake.Milestones(ctx)
}

func (c *countForge) setErr(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

func (c *countForge) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.polls
}

func newForge(issues ...forge.Issue) *countForge {
	return &countForge{Fake: &fake.Fake{
		RepoName: repoName,
		Ms:       []forge.Milestone{{Number: 1, Title: "v0.1.0", OpenCount: len(issues), ClosedCount: 1}},
		Is:       issues,
		Writers:  map[string]bool{"team": true},
	}}
}

func accepted(n int) forge.Issue {
	return forge.Issue{Number: n, Title: "t", Labels: []string{"accepted"}, Author: "team", Milestone: "v0.1.0", State: "open"}
}

// shortDir is a short temp dir: a unix socket path has a limit of 104 bytes on macOS.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "td")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return filepath.Join(d, "daemon")
}

type harness struct {
	cfg    Config
	runs   *fakeRuns
	forge  *countForge
	ci     *fake.CI
	cancel context.CancelFunc
	done   chan error
}

func newHarness(t *testing.T, f *countForge) *harness {
	t.Helper()
	home := t.TempDir()
	h := &harness{runs: &fakeRuns{home: home}, forge: f, ci: &fake.CI{}}
	h.cfg = Config{
		Home: home, Dir: shortDir(t), PollInterval: 10 * time.Millisecond, Workers: 3,
		Issue: true, CIFailure: true, ShutdownWait: time.Second,
		Repos: []Repo{{
			Name: repoName, Forge: f, CI: h.ci, Comments: h.ci, Runs: h.runs,
			AcceptedLabel: "accepted", Workflow: "issue-to-merge", DefaultBranch: "main", CIWorkflow: "tests.yaml",
		}},
	}
	return h
}

func (h *harness) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done = cancel, make(chan error, 1)
	d := New(h.cfg)
	go func() { h.done <- d.Run(ctx) }()
	waitFor(t, "socket", func() bool { _, err := os.Stat(SocketPath(h.cfg.Dir)); return err == nil })
	t.Cleanup(func() { h.stop(t) })
}

func (h *harness) stop(t *testing.T) {
	t.Helper()
	if h.cancel == nil {
		return
	}
	h.cancel()
	h.cancel = nil
	select {
	case err := <-h.done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("daemon did not stop")
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitPolls waits until the forge saw n more polls.
func waitPolls(t *testing.T, f *countForge, n int) {
	t.Helper()
	from := f.count()
	waitFor(t, "polls", func() bool { return f.count() >= from+n })
}

func TestDaemonIssueTriggerStartsRunOnce(t *testing.T) {
	h := newHarness(t, newForge(accepted(5)))
	h.start(t)
	waitFor(t, "start", func() bool { return len(h.runs.starts()) == 1 })
	waitPolls(t, h.forge, 5) // the run is done; the run file stops a second start
	got := h.runs.starts()
	if len(got) != 1 || got[0].Kind != KindIssue || got[0].Issue != 5 || got[0].Workflow != "issue-to-merge" {
		t.Fatalf("starts %+v", got)
	}
}

func TestDaemonIgnoresIssueWithoutAcceptedLabel(t *testing.T) {
	is := accepted(5)
	is.Labels = []string{"bug"}
	h := newHarness(t, newForge(is, accepted(6)))
	h.start(t)
	waitPolls(t, h.forge, 3)
	if got := h.runs.starts(); len(got) != 1 || got[0].Issue != 6 {
		t.Fatalf("starts %+v, want only #6", got)
	}
}

func TestDaemonIgnoresIssueFromNonTeamAuthor(t *testing.T) {
	is := accepted(5)
	is.Author = "stranger"
	h := newHarness(t, newForge(is, accepted(6)))
	h.start(t)
	waitPolls(t, h.forge, 3)
	if got := h.runs.starts(); len(got) != 1 || got[0].Issue != 6 {
		t.Fatalf("starts %+v, want only #6", got)
	}
}

func TestDaemonCIFailureStartsFixCI(t *testing.T) {
	h := newHarness(t, newForge())
	h.cfg.Issue = false
	h.ci.SetRun("tests.yaml", "main", forge.CIRun{ID: 1, SHA: "abc", Failed: true, CreatedAt: time.Now().Add(-time.Hour)})
	h.start(t)
	waitFor(t, "start", func() bool { return len(h.runs.starts()) == 1 })
	got := h.runs.starts()[0]
	if got.Kind != KindCIFailure || got.Workflow != FixCIWorkflow || got.Key != "ci_failure:o/r:main@abc" {
		t.Fatalf("start %+v", got)
	}
}

func TestDaemonCIGreenStartsNothing(t *testing.T) {
	h := newHarness(t, newForge(accepted(5)))
	h.ci.SetRun("tests.yaml", "main", forge.CIRun{ID: 1, SHA: "abc", CreatedAt: time.Now()})
	h.start(t)
	waitFor(t, "issue start", func() bool { return len(h.runs.starts()) == 1 })
	waitPolls(t, h.forge, 3)
	for _, s := range h.runs.starts() {
		if s.Kind == KindCIFailure {
			t.Fatalf("green CI started %+v", s)
		}
	}
}

func TestDaemonCIFailureSameSHANotRepeated(t *testing.T) {
	h := newHarness(t, newForge())
	h.cfg.Issue = false
	h.runs.result = Result{Status: "failed", Reason: "no fix"}
	red := time.Now().Add(-time.Hour)
	h.ci.SetRun("tests.yaml", "main", forge.CIRun{ID: 1, SHA: "abc", Failed: true, CreatedAt: red})
	h.start(t)
	waitFor(t, "first fix-ci", func() bool { return len(h.runs.starts()) == 1 })
	// The fix-ci run failed. The same red run must not start another one.
	time.Sleep(100 * time.Millisecond)
	if n := len(h.runs.starts()); n != 1 {
		t.Fatalf("%d starts for one red run", n)
	}
	// A new red run, after the fix-ci run, is a new failure.
	h.ci.SetRun("tests.yaml", "main", forge.CIRun{ID: 2, SHA: "def", Failed: true, CreatedAt: time.Now().Add(time.Second)})
	waitFor(t, "second fix-ci", func() bool { return len(h.runs.starts()) == 2 })
}

func TestDaemonCronTickRunsDueJob(t *testing.T) {
	cronDir := t.TempDir()
	exe := filepath.Join(cronDir, "fake-tyci")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, _ := cron.Load(cronDir)
	if err := f.Add(cron.Job{Name: "due", Prompt: "p", Dir: cronDir, Schedule: "every 1h"}); err != nil {
		t.Fatal(err)
	}
	if err := cron.Save(cronDir, f); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, newForge())
	h.cfg.Repos = nil // no repos: cron only
	h.cfg.CronInterval = 10 * time.Millisecond
	r := &cron.Runner{ConfigDir: cronDir, Exe: exe}
	h.cfg.Cron = func(ctx context.Context) error { _, err := r.Tick(ctx); return err }
	h.start(t)
	waitFor(t, "cron job log", func() bool {
		b, err := os.ReadFile(cron.LogPath(cronDir, "due"))
		return err == nil && strings.Contains(string(b), "ok")
	})
}

func TestDaemonRespectsWorkerLimit(t *testing.T) {
	h := newHarness(t, newForge(accepted(1), accepted(2), accepted(3), accepted(4), accepted(5)))
	h.runs.hold = make(chan struct{})
	h.start(t)
	waitFor(t, "3 starts", func() bool { return len(h.runs.starts()) == 3 })
	waitPolls(t, h.forge, 3)
	if n := len(h.runs.starts()); n != 3 {
		t.Fatalf("%d runs started with workers 3", n)
	}
	close(h.runs.hold)
	waitFor(t, "5 starts", func() bool { return len(h.runs.starts()) == 5 })
	waitFor(t, "runs end", func() bool { h.runs.mu.Lock(); defer h.runs.mu.Unlock(); return h.runs.running == 0 })
	h.runs.mu.Lock()
	defer h.runs.mu.Unlock()
	if h.runs.maxRuns > 3 {
		t.Fatalf("%d runs at once, limit 3", h.runs.maxRuns)
	}
}

func TestDaemonSecondInstanceRefused(t *testing.T) {
	h := newHarness(t, newForge())
	h.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := New(h.cfg).Run(ctx)
	if !errors.Is(err, ErrRunning) || !strings.Contains(err.Error(), "pid ") {
		t.Fatalf("second daemon: %v", err)
	}
	// The first daemon still answers.
	if _, err := (&Client{Path: SocketPath(h.cfg.Dir)}).Status(context.Background()); err != nil {
		t.Fatalf("first daemon: %v", err)
	}
}

func TestDaemonSocketPermissions(t *testing.T) {
	h := newHarness(t, newForge())
	if err := os.MkdirAll(h.cfg.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	for path, want := range map[string]os.FileMode{h.cfg.Dir: 0o700, SocketPath(h.cfg.Dir): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: mode %o, want %o", path, got, want)
		}
	}
	h.stop(t)
	if _, err := os.Stat(SocketPath(h.cfg.Dir)); !os.IsNotExist(err) {
		t.Errorf("socket left after stop: %v", err)
	}
}

func TestDaemonRPCStatusAndRunsList(t *testing.T) {
	h := newHarness(t, newForge(accepted(5)))
	h.runs.hold = make(chan struct{})
	defer h.runs.release(t)
	// A paused run from before: runs.list shows its ask.
	paused := &flow.RunState{Version: 1, Run: "100", Workflow: "issue-to-merge", Repo: repoName, Issue: 9, Status: "paused", Ask: &flow.Ask{Message: "which way?"}}
	if err := (&flow.Store{Dir: flow.RunDir(h.cfg.Home, "r", "100")}).Save(paused); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	waitFor(t, "start", func() bool { return len(h.runs.starts()) == 1 })
	c := &Client{Path: SocketPath(h.cfg.Dir)}
	ctx := context.Background()
	var st Status
	waitFor(t, "status shows the run", func() bool {
		var err error
		st, err = c.Status(ctx)
		return err == nil && len(st.Running) == 1
	})
	if st.PID != os.Getpid() || st.Workers != 3 || len(st.Repos) != 1 || st.Running[0].Issue != 5 || st.Running[0].Trigger != KindIssue {
		t.Fatalf("status %+v", st)
	}
	runs, err := c.RunsList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sawPaused, sawRunning bool
	for _, r := range runs {
		sawPaused = sawPaused || (r.Run == "100" && r.Status == "paused" && r.Ask == "which way?")
		sawRunning = sawRunning || (r.Issue == 5 && r.Status == "running")
	}
	if !sawPaused || !sawRunning {
		t.Fatalf("runs.list %+v", runs)
	}
	got, err := c.RunsGet(ctx, "100")
	if err != nil || got.State.Issue != 9 || !strings.HasSuffix(got.Path, "state.json") {
		t.Fatalf("runs.get %+v %v", got, err)
	}
	var re *RemoteError
	if _, err := c.RunsGet(ctx, "999"); !errors.As(err, &re) {
		t.Fatalf("runs.get of a missing run: %v", err)
	}
}

func TestDaemonDedupSurvivesRestart(t *testing.T) {
	h := newHarness(t, newForge(accepted(5)))
	h.ci.SetRun("tests.yaml", "main", forge.CIRun{ID: 1, SHA: "abc", Failed: true, CreatedAt: time.Now().Add(-time.Hour)})
	h.start(t)
	waitFor(t, "2 starts", func() bool { return len(h.runs.starts()) == 2 })
	h.stop(t)
	// A new daemon with the same home: the run files say both are done.
	runs := &fakeRuns{home: h.cfg.Home}
	h.runs = runs
	h.cfg.Repos[0].Runs = runs
	h.start(t)
	waitPolls(t, h.forge, 3)
	if got := runs.starts(); len(got) != 0 {
		t.Fatalf("restart started %+v", got)
	}
}

func TestDaemonForgeErrorRetriesNextInterval(t *testing.T) {
	f := newForge(accepted(5))
	f.setErr(errors.New("HTTP 502"))
	h := newHarness(t, f)
	h.start(t)
	waitPolls(t, f, 2)
	if n := len(h.runs.starts()); n != 0 {
		t.Fatalf("%d starts while the forge fails", n)
	}
	st, err := (&Client{Path: SocketPath(h.cfg.Dir)}).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var sawErr bool
	for _, p := range st.Polls {
		sawErr = sawErr || strings.Contains(p.Error, "HTTP 502")
	}
	if !sawErr {
		t.Fatalf("status shows no poll error: %+v", st.Polls)
	}
	f.setErr(nil)
	waitFor(t, "start after the forge works again", func() bool { return len(h.runs.starts()) == 1 })
}

func TestDaemonPausedRunGetsOneComment(t *testing.T) {
	h := newHarness(t, newForge(accepted(5)))
	h.runs.result = Result{Status: "paused", Reason: "which way?"}
	h.start(t)
	waitFor(t, "comment", func() bool { return len(h.ci.Comments()) == 1 })
	waitPolls(t, h.forge, 3)
	cs := h.ci.Comments()
	if len(cs) != 1 || cs[0].Issue != 5 || !strings.Contains(cs[0].Body, "waits for a human: which way?") || !strings.Contains(cs[0].Body, "tyci workflow resume 1 <answer>") {
		t.Fatalf("comments %+v", cs)
	}
}

func TestDaemonStartErrorCommentsOnceAndDoesNotRetry(t *testing.T) {
	h := newHarness(t, newForge(accepted(5)))
	h.runs.startErr = errors.New("setup failed")
	h.start(t)
	waitFor(t, "comment", func() bool { return len(h.ci.Comments()) == 1 })
	waitPolls(t, h.forge, 3)
	if cs := h.ci.Comments(); len(cs) != 1 || !strings.Contains(cs[0].Body, "setup failed") {
		t.Fatalf("comments %+v", cs)
	}
}

func TestDaemonContinuesInterruptedRuns(t *testing.T) {
	h := newHarness(t, newForge())
	h.cfg.Issue, h.cfg.CIFailure = false, false
	h.runs.interrupted = []Trigger{{Kind: KindResume, Repo: repoName, Run: "7", Issue: 3, Key: "resume:o/r:7"}}
	h.start(t)
	waitFor(t, "resume", func() bool { s := h.runs.starts(); return len(s) == 1 && s[0].Run == "7" })
}

func TestDaemonShutdownLeavesRunsRunning(t *testing.T) {
	h := newHarness(t, newForge(accepted(5)))
	h.cfg.ShutdownWait = 50 * time.Millisecond
	h.runs.hold = make(chan struct{})
	defer h.runs.release(t)
	h.start(t)
	waitFor(t, "start", func() bool { return len(h.runs.starts()) == 1 })
	var stopped atomic.Bool
	go func() { h.stop(t); stopped.Store(true) }()
	waitFor(t, "stop after ShutdownWait", stopped.Load)
	if cs := h.ci.Comments(); len(cs) != 0 {
		t.Fatalf("a shutdown commented: %+v", cs)
	}
}
