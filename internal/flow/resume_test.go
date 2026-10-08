package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// deadPID returns the PID of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("true not runnable: ", err)
	}
	return cmd.ProcessState.Pid()
}

// saveRun writes a running run of issue in repo o/r with a dead owner; mod edits it first.
func saveRun(t *testing.T, home string, issue int, mod func(*RunState)) *RunState {
	t.Helper()
	st := &RunState{
		Version: 1, Run: fmt.Sprintf("20260101-000000-%d", issue), Workflow: "demo", Repo: "o/r", Issue: issue,
		Status: "running", Current: "c", Worktree: t.TempDir(), PID: deadPID(t),
		Visits: map[string]int{"c": 2}, History: []Step{},
	}
	if mod != nil {
		mod(st)
	}
	if err := (&Store{Dir: RunDir(home, "r", st.Run)}).Save(st); err != nil {
		t.Fatal(err)
	}
	return st
}

func loadRunState(t *testing.T, home, run string) *RunState {
	t.Helper()
	st, err := Load(RunDir(home, "r", run))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func runIDs(sts []*RunState) []string {
	var out []string
	for _, st := range sts {
		out = append(out, st.Run)
	}
	return out
}

func hasResumedStep(st *RunState) bool {
	for _, h := range st.History {
		if h.Kind == "resume" && h.Key == "resumed" {
			return true
		}
	}
	return false
}

func TestOwnerGone_DeadPID(t *testing.T) {
	if !ownerGone(&RunState{Status: "running", PID: deadPID(t)}) {
		t.Fatal("dead PID: want gone")
	}
}

func TestOwnerGone_LivePID(t *testing.T) {
	if ownerGone(&RunState{Status: "running", PID: os.Getpid()}) {
		t.Fatal("live PID: want not gone")
	}
	if ownerGone(&RunState{Status: "paused", PID: deadPID(t)}) {
		t.Fatal("paused run: want not gone")
	}
}

func TestScanResumable_SkipsDoneAskFailed(t *testing.T) {
	home := t.TempDir()
	want := saveRun(t, home, 1, nil)
	saveRun(t, home, 2, func(st *RunState) { st.PID = os.Getpid() })
	for i, s := range []string{"done", "paused", "failed"} {
		saveRun(t, home, 3+i, func(st *RunState) { st.Status = s })
	}
	saveRun(t, home, 9, func(st *RunState) { st.Repo = "o/other" })
	got, err := ScanResumable(RunsDir(home), "o/r")
	if err != nil || len(got) != 1 || got[0].Run != want.Run {
		t.Fatalf("got %v, err %v", runIDs(got), err)
	}
}

func TestScanAsk_ReturnsOnlyAsk(t *testing.T) {
	home := t.TempDir()
	saveRun(t, home, 1, nil)
	want := saveRun(t, home, 2, func(st *RunState) { st.Status = "paused" })
	saveRun(t, home, 3, func(st *RunState) { st.Status = "done" })
	saveRun(t, home, 4, func(st *RunState) { st.Status = "paused"; st.Repo = "o/other" })
	got, err := ScanAsk(RunsDir(home), "o/r")
	if err != nil || len(got) != 1 || got[0].Run != want.Run {
		t.Fatalf("got %v, err %v", runIDs(got), err)
	}
}

func TestAskUnfinished_ListsPausedRunUnchanged(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	st := saveRun(t, e.home, 1, func(st *RunState) {
		st.Status, st.Current, st.Ask = "paused", "ask", &Ask{Message: "Need a decision."}
	})
	before := loadRunState(t, e.home, st.Run)
	e.m.AskUnfinished()
	got := e.notice(t)
	for _, want := range []string{"run " + st.Run + ", issue #1", "reason: Need a decision.", "answers: retry, stop\n", "Nothing resumes until the user answers"} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice = %q, want %q", got, want)
		}
	}
	after := loadRunState(t, e.home, st.Run)
	if after.Status != "paused" || !after.UpdatedAt.Equal(before.UpdatedAt) || len(e.m.active) != 0 {
		t.Fatalf("paused run changed: %+v", after)
	}
}

func TestResume_DoesNotIncrementVisits(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	st := saveRun(t, e.home, 1, nil)
	if id, _, err := e.m.Start(context.Background(), StartRequest{Issue: 1}); err != nil || id != st.Run {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	if got := e.notice(t); !strings.HasPrefix(got, "workflow run "+st.Run) {
		t.Fatalf("notice = %q", got)
	}
	after := loadRunState(t, e.home, st.Run)
	if after.Status != "done" || after.Visits["c"] != 2 || after.Resumed != 1 || !hasResumedStep(after) {
		t.Fatalf("after = %+v", after)
	}
	if after.PID != os.Getpid() {
		t.Fatalf("pid = %d, want %d", after.PID, os.Getpid())
	}
}

// A run paused at start-up continues at its saved state like a restart: the
// visit counts stay, so the answer "resume" does not count a new visit.
func TestResume_StartupAnswerKeepsVisits(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	st := saveRun(t, e.home, 1, nil)
	e.m.AskUnfinished()
	e.notice(t)
	if err := e.m.Resume(st.Run, "resume"); err != nil {
		t.Fatal(err)
	}
	e.notice(t)
	after := loadRunState(t, e.home, st.Run)
	if after.Status != "done" || after.Visits["c"] != 2 {
		t.Fatalf("after = %+v", after)
	}
}

// Only the goto to the saved state restarts. A start-up "retry" leaves the
// ask state for a new visit of c, so the visit counts reset as before.
func TestResume_StartupRetryResetsVisits(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	st := saveRun(t, e.home, 1, nil)
	e.m.AskUnfinished()
	e.notice(t)
	if err := e.m.Resume(st.Run, "retry"); err != nil {
		t.Fatal(err)
	}
	e.notice(t)
	after := loadRunState(t, e.home, st.Run)
	if after.Status != "done" || after.Visits["c"] != 1 {
		t.Fatalf("after = %+v", after)
	}
}

func TestResume_CapAtThree(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	st := saveRun(t, e.home, 1, func(st *RunState) { st.Resumed = maxResumes })
	if _, _, err := e.m.Start(context.Background(), StartRequest{Issue: 1}); err == nil {
		t.Fatal("start: want an error")
	}
	if got := e.notice(t); !strings.Contains(got, "paused: resumed 3 times, please check") {
		t.Fatalf("notice = %q", got)
	}
	after := loadRunState(t, e.home, st.Run)
	if after.Status != "paused" || after.Current != "ask" || after.Resumed != maxResumes || after.Ask.Reason != "resume:c" {
		t.Fatalf("after = %+v ask %+v", after, after.Ask)
	}
	if len(e.m.active) != 0 {
		t.Fatal("run started")
	}
}

func TestResume_MissingWorktreeGoesToAsk(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	st := saveRun(t, e.home, 1, func(st *RunState) { st.Worktree = filepath.Join(e.home, "gone") })
	if _, _, err := e.m.Start(context.Background(), StartRequest{Issue: 1}); err == nil {
		t.Fatal("start: want an error")
	}
	if got := e.notice(t); !strings.Contains(got, "is missing") {
		t.Fatalf("notice = %q", got)
	}
	after := loadRunState(t, e.home, st.Run)
	if after.Status != "paused" || after.Current != "ask" {
		t.Fatalf("after = %+v", after)
	}
	if _, err := os.Stat(st.Worktree); !os.IsNotExist(err) {
		t.Fatal("worktree was recreated")
	}
}

// Several instances scan the same run with a dead owner and resume it at once.
// The run lock lets only one of them claim it; the others see the live owner.
func TestResume_TwoInstancesClaimOnce(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	st := saveRun(t, e.home, 1, nil)
	info, err := e.m.Info()
	if err != nil {
		t.Fatal(err)
	}
	const instances = 6
	var claims atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range instances {
		m := &Manager{Info: e.m.Info, Workflow: e.m.Workflow, NewRunner: e.m.NewRunner, Notify: e.m.Notify}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			m.mu.Lock()
			ok, err := m.resumeRun(info, st)
			m.mu.Unlock()
			if err != nil {
				t.Errorf("resume: %v", err)
			}
			if ok {
				claims.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if n := claims.Load(); n != 1 {
		t.Fatalf("claims = %d, want 1", n)
	}
	close(c.release)
	e.notice(t)
	if after := loadRunState(t, e.home, st.Run); after.Resumed != 1 {
		t.Fatalf("resumed = %d, want 1", after.Resumed)
	}
}

func TestStateWrite_Atomic(t *testing.T) {
	home := t.TempDir()
	st := saveRun(t, home, 1, nil)
	dir := RunDir(home, "r", st.Run)
	for _, name := range []string{"state.json.tmp", "state.json.tmp-123"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"version":1,"run":`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := loadRunState(t, home, st.Run); got.Run != st.Run {
		t.Fatalf("run = %q", got.Run)
	}
	got, err := ScanResumable(RunsDir(home), "o/r")
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, err %v", runIDs(got), err)
	}
	info, err := os.Stat(filepath.Join(dir, stateFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, err %v", info.Mode(), err)
	}
}

func TestManager_StartAdoptsResumedRun(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	st := saveRun(t, e.home, 5, nil)
	e.m.AskUnfinished()
	e.notice(t)
	if err := e.m.Resume(st.Run, "resume"); err != nil {
		t.Fatal(err)
	}
	id, _, err := e.m.Start(context.Background(), StartRequest{Issue: 5})
	if err != nil || id != st.Run {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	if _, _, err := e.m.Start(context.Background(), StartRequest{Issue: 5}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second start: err = %v", err)
	}
	close(c.release)
	e.notice(t)
}

func TestAskUnfinished_TwoRunsOneQuestion(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	first := saveRun(t, e.home, 1, func(st *RunState) {
		st.PR = 12
		st.History = []Step{{Seq: 1, State: "c", Kind: "check", Key: "ok"}}
	})
	second := saveRun(t, e.home, 2, nil)
	e.m.AskUnfinished()
	got := e.notice(t)
	for _, want := range []string{
		"2 workflow run(s) of o/r",
		"run " + first.Run + ", issue #1, last step c -> ok, PR #12",
		"run " + second.Run + ", issue #2",
		"answers: resume, retry, stop",
		"workflow_resume",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice = %q, want %q", got, want)
		}
	}
	select {
	case s := <-e.notices:
		t.Fatalf("second notice %q", s)
	default:
	}
	for _, st := range []*RunState{first, second} {
		after := loadRunState(t, e.home, st.Run)
		if after.Status != "paused" || after.Current != "ask" || after.Ask.Reason != "resume:c" || after.Resumed != 0 {
			t.Fatalf("after = %+v ask %+v", after, after.Ask)
		}
	}
	if len(e.m.active) != 0 {
		t.Fatal("a run resumed before the answer")
	}
	// The scheduler cannot start a new run while the answer is open.
	if _, _, err := e.m.Start(context.Background(), StartRequest{Issue: 2}); !errors.Is(err, ErrBusy) {
		t.Fatalf("start: err = %v", err)
	}

	if err := e.m.Resume(first.Run, "resume"); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Resume(second.Run, "stop"); err != nil {
		t.Fatal(err)
	}
	if got := e.notice(t); !strings.Contains(got, second.Run) {
		t.Fatalf("notice = %q", got)
	}
	if got := loadRunState(t, e.home, second.Run); got.Status != "done" {
		t.Fatalf("stopped run = %+v", got)
	}
	close(c.release)
	if got := e.notice(t); !strings.Contains(got, first.Run) {
		t.Fatalf("notice = %q", got)
	}
	after := loadRunState(t, e.home, first.Run)
	if after.Status != "done" || after.History[len(after.History)-2].Key != "goto c" {
		t.Fatalf("resumed run = %+v", after)
	}
}

func TestAskUnfinished_NothingUnfinished(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	saveRun(t, e.home, 1, func(st *RunState) { st.Status = "done" })
	e.m.AskUnfinished()
	select {
	case s := <-e.notices:
		t.Fatalf("notice %q", s)
	default:
	}
}

func TestResume_ResumeOnlyForRestartPause(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	st := saveRun(t, e.home, 1, func(st *RunState) {
		st.Status, st.Current, st.Ask = "paused", "ask", &Ask{Message: "Need a decision."}
	})
	if err := e.m.Resume(st.Run, "resume"); err == nil || !strings.Contains(err.Error(), "unknown answer") {
		t.Fatalf("err = %v", err)
	}
}

// freezeStore stops writing at the nth save whose state is state with the visit
// count visits, and cancels the run: the file on disk then looks like tyci was
// killed at that point.
type freezeStore struct {
	*Store
	state  string
	cancel context.CancelFunc
	visits int
	nth    int
	seen   int
	frozen bool
}

func (f *freezeStore) Save(st *RunState) error {
	if f.frozen {
		return nil
	}
	if err := f.Store.Save(st); err != nil {
		return err
	}
	if st.Current == f.state && st.Visits[f.state] == f.visits {
		f.seen++
		if f.seen == f.nth {
			f.frozen = true
			f.cancel()
		}
	}
	return nil
}

func TestResume_EndToEnd_StubScripts(t *testing.T) {
	// Killed in ci after the visit of ci was saved.
	resumeKilledInCI(t, happy(), nil, freezeStore{state: "ci", visits: 1, nth: 1}, 1)
}

// Killed between the transition into ci and the visit save: the file has no
// counted visit of ci. The resumed run counts it, so ci has one visit.
func TestResume_EndToEnd_LostVisitCounted(t *testing.T) {
	resumeKilledInCI(t, happy(), nil, freezeStore{state: "ci", visits: 0, nth: 1}, 1)
}

// Killed between the second transition into ci (oracle goto:ci) and its visit
// save. The file has one counted visit of ci; the resumed run counts the second.
func TestResume_EndToEnd_LostVisitCountedSecondEntry(t *testing.T) {
	s := happy()
	s["fixer"] = []string{"failed"}
	s["oracle"] = []string{"goto:ci the PR head moved, wait for CI again"}
	setup := func(e *e2e) { e.ctlSet("merge_fail_once", "") }
	resumeKilledInCI(t, s, setup, freezeStore{state: "ci", visits: 1, nth: 2}, 2)
}

// resumeKilledInCI runs the e2e workflow with script until fz freezes it in ci,
// then resumes the run through Manager.Start and checks the final visit count
// of ci. setup, when set, prepares the stub gh before the run.
func resumeKilledInCI(t *testing.T, script map[string][]string, setup func(*e2e), fz freezeStore, wantVisits int) {
	e := newE2E(t, script)
	if setup != nil {
		setup(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := e.newRunner()
	fz.Store, fz.cancel = &Store{Dir: e.runDir}, cancel
	r.Store = &fz
	_ = r.Run(ctx, e.st)

	// The killed process: state ci, status running, owner gone.
	st, err := Load(e.runDir)
	if err != nil || st.Current != "ci" || st.Status != "running" {
		t.Fatalf("saved state = %+v, err %v", st, err)
	}
	st.PID = deadPID(t)
	if err := (&Store{Dir: e.runDir}).Save(st); err != nil {
		t.Fatal(err)
	}
	visitsBefore := fmt.Sprint(st.Visits)

	notices := make(chan string, 8)
	m := &Manager{
		Info: func() (RepoInfo, error) {
			return RepoInfo{Home: e.home, Root: e.work, Repo: e2eRepo, DefaultBranch: "main"}, nil
		},
		Workflow: func(RepoInfo, string) (*Workflow, error) { return e.wf, nil },
		NewRunner: func(RepoInfo, *Workflow, *RunState) *Runner {
			return e.newRunner()
		},
		Notify: func(s string) { notices <- s },
	}
	if id, _, err := m.Start(context.Background(), StartRequest{Issue: st.Issue}); err != nil || id != st.Run {
		t.Fatalf("start: id = %q, err = %v", id, err)
	}
	for _, want := range []string{" done: merged "} {
		select {
		case got := <-notices:
			if !strings.Contains(got, want) {
				t.Fatalf("notice = %q, want %q", got, want)
			}
		case <-time.After(60 * time.Second):
			t.Fatalf("no notice %q", want)
		}
	}
	e.st, err = Load(e.runDir)
	if err != nil {
		t.Fatal(err)
	}
	if e.st.Status != "done" || !WasMerged(e.st) || !hasResumedStep(e.st) || e.st.Resumed != 1 {
		t.Fatalf("status %s, resumed %d, history %v", e.st.Status, e.st.Resumed, e.trail())
	}
	if got := e.st.Visits["ci"]; got != wantVisits {
		t.Fatalf("visits ci = %d, want %d (before %s)", got, wantVisits, visitsBefore)
	}
	e.wantMerged()
}

// #403: the run is killed in post_review after its review step. The workflow
// file then renames the review state. The resumed run still posts the review,
// because the history keeps the role of the review step.
func TestResume_ReviewStateRenamed_StillPostsReview(t *testing.T) {
	e := newE2E(t, happy())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := e.newRunner()
	r.Store = &freezeStore{Store: &Store{Dir: e.runDir}, state: "post_review", visits: 1, nth: 1, cancel: cancel}
	_ = r.Run(ctx, e.st)

	st, err := Load(e.runDir)
	if err != nil || st.Current != "post_review" || st.Status != "running" {
		t.Fatalf("saved state = %+v, err %v", st, err)
	}
	st.PID = deadPID(t)
	if err := (&Store{Dir: e.runDir}).Save(st); err != nil {
		t.Fatal(err)
	}
	renameState(e.wf, "review", "judge")

	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()
	if err := e.newRunner().Continue(ctx2, st); err != nil {
		t.Fatalf("continue: %v (status %s, reason %q)", err, st.Status, st.Reason)
	}
	if st.Status != "done" || !WasMerged(st) {
		t.Fatalf("status %s, reason %q", st.Status, st.Reason)
	}
	for _, h := range st.History {
		if h.State == "findings" && (h.Role != "review" || h.Task != "findings_to_issues") {
			t.Errorf("findings step has role %q, task %q", h.Role, h.Task)
		}
	}
	body, _ := os.ReadFile(filepath.Join(e.ctl, "review_body"))
	if !strings.Contains(string(body), "ACCEPT") {
		t.Errorf("posted review = %q", body)
	}
}

func TestManager_AdoptReturnsResumedRunOnce(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	st := saveRun(t, e.home, 5, nil)
	e.m.AskUnfinished()
	e.notice(t)
	if err := e.m.Resume(st.Run, "resume"); err != nil {
		t.Fatal(err)
	}
	if got := e.m.Adoptable(); len(got) != 1 || got[0] != 5 {
		t.Fatalf("adoptable = %v", got)
	}
	if id, paused, ok := e.m.Adopt(5); !ok || paused || id != st.Run {
		t.Fatalf("adopt = %q, %v, %v", id, paused, ok)
	}
	if got := e.m.Adoptable(); len(got) != 0 {
		t.Fatalf("adoptable after adopt = %v", got)
	}
	if _, _, ok := e.m.Adopt(5); ok {
		t.Fatal("second adopt returned the run")
	}
	close(c.release)
	e.notice(t)
}

// The orchestrator adopts a run paused at start-up before the answer, so a
// later answer ("resume" or "stop") never lets it start a second run.
func TestManager_AdoptReturnsPausedRunOnce(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	st := saveRun(t, e.home, 5, nil)
	saveRun(t, e.home, 6, func(st *RunState) { st.Status = "done" })
	e.m.AskUnfinished()
	e.notice(t)
	if got := e.m.Adoptable(); len(got) != 1 || got[0] != 5 {
		t.Fatalf("adoptable = %v", got)
	}
	if id, paused, ok := e.m.Adopt(5); !ok || !paused || id != st.Run {
		t.Fatalf("adopt = %q, %v, %v", id, paused, ok)
	}
	if got := e.m.Adoptable(); len(got) != 0 {
		t.Fatalf("adoptable after adopt = %v", got)
	}
	if _, _, ok := e.m.Adopt(5); ok {
		t.Fatal("second adopt returned the run")
	}
	if err := e.m.Resume(st.Run, "resume"); err != nil {
		t.Fatal(err)
	}
	// The adopted run is not offered again after the answer.
	if got := e.m.Adoptable(); len(got) != 0 {
		t.Fatalf("adoptable after resume = %v", got)
	}
	if _, _, err := e.m.Start(context.Background(), StartRequest{Issue: 5}); !errors.Is(err, ErrBusy) {
		t.Fatalf("start: err = %v", err)
	}
	close(c.release)
	e.notice(t)
}

// An adopted run that ends is forgotten by the Manager, so the adopted set
// does not grow with every run.
func TestManager_AdoptedRunForgottenWhenDone(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	st := saveRun(t, e.home, 5, nil)
	e.m.AskUnfinished()
	e.notice(t)
	if _, paused, ok := e.m.Adopt(5); !ok || !paused {
		t.Fatalf("adopt: paused %v, ok %v", paused, ok)
	}
	if err := e.m.Resume(st.Run, "resume"); err != nil {
		t.Fatal(err)
	}
	done := make(chan RunEvent, 8)
	unsub := e.m.Subscribe(func(ev RunEvent) { done <- ev })
	defer unsub()
	close(c.release)
	for {
		select {
		case ev := <-done:
			if ev.Status == "done" {
				e.m.mu.Lock()
				n := len(e.m.adopted)
				e.m.mu.Unlock()
				if n != 0 {
					t.Fatalf("adopted after the run ended: %d", n)
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("run did not end")
		}
	}
}

// A start-up "resume" is refused while orchestrator.workers runs are active. The
// refused run stays paused, and its resume works once a run has ended.
func TestResume_StartupRefusedAtWorkersLimit(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	e.m.SetWorkers(1)
	first := saveRun(t, e.home, 5, nil)
	second := saveRun(t, e.home, 6, nil)
	e.m.AskUnfinished()
	e.notice(t)
	ended := make(chan RunEvent, 8)
	unsub := e.m.Subscribe(func(ev RunEvent) { ended <- ev })
	defer unsub()
	waitDone := func() {
		t.Helper()
		for {
			select {
			case ev := <-ended:
				if ev.Status == "done" {
					return
				}
			case <-time.After(5 * time.Second):
				t.Fatal("run did not end")
			}
		}
	}
	if err := e.m.Resume(first.Run, "resume"); err != nil {
		t.Fatal(err)
	}
	err := e.m.Resume(second.Run, "resume")
	if err == nil || !strings.Contains(err.Error(), "orchestrator.workers is 1") {
		t.Fatalf("second resume: err = %v", err)
	}
	if st := loadRunState(t, e.home, second.Run); st.Status != "paused" {
		t.Fatalf("refused run: status %q", st.Status)
	}
	close(c.release)
	waitDone()
	if err := e.m.Resume(second.Run, "resume"); err != nil {
		t.Fatalf("resume after a run ended: %v", err)
	}
	waitDone()
}

// A workflow without an ask state cannot pause a run at start-up: the run
// fails, and the user gets a notice for it.
func TestAskUnfinished_NoAskStateNotifiesFail(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	wf := e.m.Workflow
	e.m.Workflow = func(info RepoInfo, name string) (*Workflow, error) {
		w, err := wf(info, name)
		if err != nil {
			return nil, err
		}
		cp := *w
		cp.States = map[string]State{}
		for k, v := range w.States {
			if k != "ask" {
				cp.States[k] = v
			}
		}
		return &cp, nil
	}
	st := saveRun(t, e.home, 1, nil)
	e.m.AskUnfinished()
	if got := e.notice(t); !strings.Contains(got, st.Run) || !strings.Contains(got, "failed: tyci restarted") {
		t.Fatalf("notice = %q", got)
	}
	if got := loadRunState(t, e.home, st.Run); got.Status != "failed" {
		t.Fatalf("run = %+v", got)
	}
}

// A run resumed by a later Start takes a worker slot. With no free slot the
// resume is refused and the stale run keeps its saved state.
func TestWorkflowStart_ResumeRefusedAtWorkersLimit(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	e.m.SetWorkers(1)
	if _, _, err := e.m.Start(context.Background(), StartRequest{Issue: 1}); err != nil {
		t.Fatal(err)
	}
	st := saveRun(t, e.home, 6, nil)
	_, _, err := e.m.Start(context.Background(), StartRequest{Issue: 6})
	if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "orchestrator.workers is 1") {
		t.Fatalf("err = %v", err)
	}
	if got := loadRunState(t, e.home, st.Run); got.Resumed != 0 || got.Status != "running" {
		t.Fatalf("refused run changed: %+v", got)
	}
	close(c.release)
	e.notice(t)
}

// A paused run is adoptable at start-up, but a later fill takes only the runs a
// resume made active (AdoptableResumed), so a paused run of a manual start does
// not take a worker slot.
func TestAdoptableResumed_SkipsPausedRuns(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	saveRun(t, e.home, 5, func(st *RunState) {
		st.Status, st.Current, st.Ask = "paused", "ask", &Ask{Message: "Need a decision."}
	})
	if got := e.m.Adoptable(); len(got) != 1 || got[0] != 5 {
		t.Fatalf("adoptable = %v", got)
	}
	if got := e.m.AdoptableResumed(); len(got) != 0 {
		t.Fatalf("adoptable resumed = %v", got)
	}
}

// A run resumed by hand through Start is adoptable, so the orchestrator takes
// its slot at the next fill.
func TestWorkflowStart_ResumedByHandIsAdoptable(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	st := saveRun(t, e.home, 5, nil)
	id, _, err := e.m.Start(context.Background(), StartRequest{Issue: 5})
	if err != nil || id != st.Run {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	if got := e.m.Adoptable(); len(got) != 1 || got[0] != 5 {
		t.Fatalf("adoptable = %v", got)
	}
	if got := e.m.AdoptableResumed(); len(got) != 1 || got[0] != 5 {
		t.Fatalf("adoptable resumed = %v", got)
	}
	if id, paused, ok := e.m.Adopt(5); !ok || paused || id != st.Run {
		t.Fatalf("adopt = %q, %v, %v", id, paused, ok)
	}
	close(c.release)
	e.notice(t)
}
