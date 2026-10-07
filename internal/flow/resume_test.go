package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestResumeAll_ReshowsAskNotice(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	st := saveRun(t, e.home, 1, func(st *RunState) {
		st.Status, st.Current, st.Ask = "paused", "ask", &Ask{Message: "Need a decision."}
	})
	before := loadRunState(t, e.home, st.Run)
	e.m.ResumeAll(0)
	if got := e.notice(t); !strings.Contains(got, st.Run+" paused: Need a decision.") {
		t.Fatalf("notice = %q", got)
	}
	after := loadRunState(t, e.home, st.Run)
	if after.Status != "paused" || !after.UpdatedAt.Equal(before.UpdatedAt) || len(e.m.active) != 0 {
		t.Fatalf("paused run changed: %+v", after)
	}
}

func TestResume_DoesNotIncrementVisits(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	st := saveRun(t, e.home, 1, nil)
	e.m.ResumeAll(0)
	if got := e.notice(t); got != "resumed run "+st.Run+" at state c" {
		t.Fatalf("notice = %q", got)
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

func TestResume_CapAtThree(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	st := saveRun(t, e.home, 1, func(st *RunState) { st.Resumed = maxResumes })
	e.m.ResumeAll(0)
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
	e.m.ResumeAll(0)
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

func TestResume_ClaimRace(t *testing.T) {
	home := t.TempDir()
	st := saveRun(t, home, 1, nil)
	dir := RunDir(home, "r", st.Run)
	a, b := loadRunState(t, home, st.Run), loadRunState(t, home, st.Run)
	// Both instances write their claim before either re-reads.
	if err := claim(dir, a, 1001); err != nil {
		t.Fatal(err)
	}
	if err := claim(dir, b, 1002); err != nil {
		t.Fatal(err)
	}
	if owns(dir, 1001) || !owns(dir, 1002) {
		t.Fatal("want exactly the last writer to continue")
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
	e.m.ResumeAll(0)
	e.notice(t)
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

func TestResumeAll_LimitLeavesRunsForStart(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	first := saveRun(t, e.home, 1, nil)
	second := saveRun(t, e.home, 2, nil)
	e.m.ResumeAll(1)
	if got := e.notice(t); got != "resumed run "+first.Run+" at state c" {
		t.Fatalf("notice = %q", got)
	}
	if got := e.notice(t); got != "run "+second.Run+" not resumed: worker limit 1 reached; start issue #2 again to resume it" {
		t.Fatalf("notice = %q", got)
	}
	if loadRunState(t, e.home, second.Run).Resumed != 0 {
		t.Fatal("run over the limit was claimed")
	}
	id, _, err := e.m.Start(context.Background(), StartRequest{Issue: 2})
	if err != nil || id != second.Run {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	if got := loadRunState(t, e.home, second.Run); got.Resumed != 1 || !hasResumedStep(got) {
		t.Fatalf("second = %+v", got)
	}
	close(c.release)
	e.notice(t)
	e.notice(t)
}

// freezeStore stops writing once the run is inside state ci and cancels the
// run: the file on disk then looks like tyci was killed during ci.
type freezeStore struct {
	*Store
	cancel context.CancelFunc
	frozen bool
}

func (f *freezeStore) Save(st *RunState) error {
	if f.frozen {
		return nil
	}
	if err := f.Store.Save(st); err != nil {
		return err
	}
	if st.Current == "ci" && st.Visits["ci"] == 1 {
		f.frozen = true
		f.cancel()
	}
	return nil
}

func TestResume_EndToEnd_StubScripts(t *testing.T) {
	e := newE2E(t, happy())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := e.newRunner()
	r.Store = &freezeStore{Store: &Store{Dir: e.runDir}, cancel: cancel}
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
	m.ResumeAll(0)
	for _, want := range []string{"resumed run " + st.Run + " at state ci", " done: merged "} {
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
	if e.st.Status != "done" || !wasMerged(e.st) || !hasResumedStep(e.st) || e.st.Resumed != 1 {
		t.Fatalf("status %s, resumed %d, history %v", e.st.Status, e.st.Resumed, e.trail())
	}
	if got := e.st.Visits["ci"]; got != 1 {
		t.Fatalf("visits ci = %d (before %s)", got, visitsBefore)
	}
	e.wantMerged()
}

func TestManager_AdoptReturnsResumedRunOnce(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	st := saveRun(t, e.home, 5, nil)
	e.m.ResumeAll(0)
	e.notice(t)
	if got := e.m.Adoptable(); len(got) != 1 || got[0] != 5 {
		t.Fatalf("adoptable = %v", got)
	}
	if id, ok := e.m.Adopt(5); !ok || id != st.Run {
		t.Fatalf("adopt = %q, %v", id, ok)
	}
	if got := e.m.Adoptable(); len(got) != 0 {
		t.Fatalf("adoptable after adopt = %v", got)
	}
	if _, ok := e.m.Adopt(5); ok {
		t.Fatal("second adopt returned the run")
	}
	close(c.release)
	e.notice(t)
}
