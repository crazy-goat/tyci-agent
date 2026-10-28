package main

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/display"
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/planner"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/tools"
)

// planGit runs git in dir and fails the test on error.
func planGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// newPlanRepo builds a bare origin and a clone "work" with one commit on main
// and origin/HEAD set. HOME points at a temp dir, so the planner worktrees
// never reach the real ~/.tyci.
func newPlanRepo(t *testing.T) (home, work string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")

	tmp := t.TempDir()
	planGit(t, tmp, "init", "--bare", "-q", "origin.git")
	work = filepath.Join(tmp, "work")
	planGit(t, tmp, "clone", "-q", "origin.git", "work")
	planGit(t, work, "checkout", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	planGit(t, work, "add", "x")
	planGit(t, work, "commit", "-qm", "commit X")
	planGit(t, work, "push", "-q", "-u", "origin", "main")
	planGit(t, work, "remote", "set-head", "origin", "main")
	return home, work
}

// planFakes replaces the planner spawn and the notice sink. The spawn stub
// records the spec and the job id, runs hook in the worktree, and returns.
type planFakes struct {
	mu      sync.Mutex
	specs   []tools.TaskSpec
	jobIDs  []string
	started chan string
	notices chan string
	hook    func(dir string)
}

// writePlanConfig gives the planner role a model. The model is not called,
// because the spawn is a stub.
func writePlanConfig(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".tyci")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"models":{"m":"test/model"},"default_model":"m"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func installPlanFakes(t *testing.T, home, work string) *planFakes {
	t.Helper()
	writePlanConfig(t, home)
	f := &planFakes{started: make(chan string, 16), notices: make(chan string, 16)}
	origInfo, origNew, origSpawn, origNotify := planRepoInfo, planNew, planSpawn, planNotify
	t.Cleanup(func() {
		planRepoInfo, planNew, planSpawn, planNotify = origInfo, origNew, origSpawn, origNotify
	})
	resetPlannerSessions(t)
	planRepoInfo = func() (flow.RepoInfo, error) {
		return flow.RepoInfo{Home: home, Root: work, MainRoot: work, Repo: work, DefaultBranch: "main"}, nil
	}
	planSpawn = func(ctx context.Context, s tools.TaskSpec) (string, string, error) {
		id, _ := ctx.Value(tools.JobIDCtxKey{}).(string)
		f.mu.Lock()
		f.specs = append(f.specs, s)
		f.jobIDs = append(f.jobIDs, id)
		hook := f.hook
		f.mu.Unlock()
		f.started <- id
		if hook != nil {
			hook(s.Dir)
		}
		return "plan", "", nil
	}
	planNotify = func(text string) { f.notices <- text }
	return f
}

// resetPlannerSessions empties the registry of open sessions for this test.
// At the end of the test the open sessions are closed, so no worktree is left
// in the temp home.
func resetPlannerSessions(t *testing.T) {
	t.Helper()
	plannerSessionsMu.Lock()
	plannerSessions = map[string]plannerEntry{}
	plannerSessionsMu.Unlock()
	t.Cleanup(func() { closePlannerSessions(io.Discard) })
}

// finish waits for the next planner job to start and to end. Its session
// stays open. It returns the job id.
func (f *planFakes) finish(t *testing.T) string {
	t.Helper()
	var id string
	select {
	case id = <-f.started:
	case <-time.After(10 * time.Second):
		t.Fatal("no planner job started")
	}
	job, ok := JobRegistry.Wait(context.Background(), id, 10*time.Second)
	if !ok || job.Status == jobs.StatusRunning {
		t.Fatalf("job %s did not end", id)
	}
	return id
}

// nextNotice returns the next notice, or fails after a timeout.
func nextNotice(t *testing.T, f *planFakes) string {
	t.Helper()
	select {
	case n := <-f.notices:
		return n
	case <-time.After(10 * time.Second):
		t.Fatal("no notice")
		return ""
	}
}

// planDisp is the fake display of a /plan command.
type planDisp struct {
	resets int
	errs   []error
}

func (d *planDisp) Error(err error)                           { d.errs = append(d.errs, err) }
func (d *planDisp) ToolBlock(string)                          {}
func (d *planDisp) ResetStatus()                              { d.resets++ }
func (d *planDisp) OpenResumePicker([]display.TuiResumeEntry) {}
func (d *planDisp) OpenBtwList()                              {}

func TestPlanCommand_StartsPlannerJobInNewWorktree(t *testing.T) {
	home, work := newPlanRepo(t)
	withTestWiring(t)
	f := installPlanFakes(t, home, work)

	disp := &planDisp{}
	handlePlanCommand(disp, " write the auth plan ", func(topic string) error {
		return startPlanner(context.Background(), topic)
	})
	if len(disp.errs) != 0 || disp.resets != 1 {
		t.Fatalf("errs = %v, resets = %d", disp.errs, disp.resets)
	}
	n := nextNotice(t, f)
	if !strings.HasPrefix(n, "planner ") {
		t.Fatalf("start notice = %q", n)
	}
	f.finish(t)

	s := f.specs[0]
	if s.Task != "write the auth plan" {
		t.Fatalf("Task = %q", s.Task)
	}
	if s.Policy == nil || s.Policy.Role != "planner" {
		t.Fatalf("Policy = %+v, want role planner", s.Policy)
	}
	if s.Policy.Permission != flowconfig.PermissionWorktreeWrite || s.Policy.Dir != s.Dir {
		t.Fatalf("Policy = %+v, want worktree-write in %s", s.Policy, s.Dir)
	}
	want := filepath.Join(home, ".tyci", "worktrees", filepath.Base(work))
	if filepath.Dir(s.Dir) != want {
		t.Fatalf("Dir = %s, want a new worktree under %s", s.Dir, want)
	}
	if s.SystemPrompt == "" {
		t.Fatal("planner job has no system prompt")
	}
	if _, err := os.Stat(s.Dir); err != nil {
		t.Fatalf("worktree %s must stay open until tyci exits: %v", s.Dir, err)
	}
	if n := len(plannerSessions); n != 1 {
		t.Fatalf("open planner sessions = %d, want 1", n)
	}
}

func TestPlanCommand_NoTopicAsksWhatToPlan(t *testing.T) {
	home, work := newPlanRepo(t)
	withTestWiring(t)
	f := installPlanFakes(t, home, work)

	if err := startPlanner(context.Background(), ""); err != nil {
		t.Fatalf("startPlanner: %v", err)
	}
	nextNotice(t, f)
	f.finish(t)
	if f.specs[0].Task != planDefaultTopic {
		t.Fatalf("Task = %q, want the default question", f.specs[0].Task)
	}
}

func TestPlanCommand_TwoPlannersHaveDistinctWorktrees(t *testing.T) {
	home, work := newPlanRepo(t)
	withTestWiring(t)
	f := installPlanFakes(t, home, work)

	for _, topic := range []string{"first", "second"} {
		if err := startPlanner(context.Background(), topic); err != nil {
			t.Fatalf("startPlanner %s: %v", topic, err)
		}
	}
	nextNotice(t, f)
	nextNotice(t, f)
	f.finish(t)
	f.finish(t)
	if len(f.specs) != 2 {
		t.Fatalf("spawned %d planners, want 2", len(f.specs))
	}
	if f.specs[0].Dir == f.specs[1].Dir {
		t.Fatalf("both planners share worktree %s", f.specs[0].Dir)
	}
	if f.jobIDs[0] == f.jobIDs[1] {
		t.Fatalf("both planners share job %s", f.jobIDs[0])
	}
}

func TestPlanCommand_NewErrorIsShownAndStartsNoJob(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	work := t.TempDir()
	planGit(t, work, "init", "-q", "-b", "main")
	writePlanConfig(t, home)
	withTestWiring(t)
	f := installPlanFakes(t, home, work)

	disp := &planDisp{}
	handlePlanCommand(disp, "topic", func(topic string) error {
		return startPlanner(context.Background(), topic)
	})
	if len(disp.errs) != 1 || !strings.Contains(disp.errs[0].Error(), "/plan:") {
		t.Fatalf("errs = %v, want one /plan error", disp.errs)
	}
	if len(f.specs) != 0 {
		t.Fatalf("a job started after planner.New failed: %+v", f.specs)
	}
	if got := JobRegistry.List(); len(got) != 0 {
		t.Fatalf("registry has %d jobs, want 0", len(got))
	}
}

// TestPlanCommand_TakesNoWorkerSlot checks that /plan never starts or changes a
// workflow run. A worker slot is counted by flow.Manager (its active runs and
// the paused runs the orchestrator holds), so only a workflow run takes one.
// The slot fixtures of internal/flow are not exported to package main, so the
// test reads the source: plancmd.go may call no workflowManager method except
// Info, and it may use no flow identifier except RepoInfo.
func TestPlanCommand_TakesNoWorkerSlot(t *testing.T) {
	_, self, _, _ := runtime.Caller(0)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(self), "plancmd.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case x.Name == "workflowManager" && sel.Sel.Name != "Info":
			t.Errorf("%s: plancmd.go uses workflowManager.%s", fset.Position(sel.Pos()), sel.Sel.Name)
		case x.Name == "flow" && sel.Sel.Name != "RepoInfo":
			t.Errorf("%s: plancmd.go uses flow.%s", fset.Position(sel.Pos()), sel.Sel.Name)
		}
		return true
	})
}

// commitIn adds a file with a commit in dir, so the worktree counts as changed.
func commitIn(t *testing.T, dir string) {
	t.Helper()
	if err := commitInErr(dir); err != nil {
		t.Fatal(err)
	}
}

// commitInErr is commitIn for a goroutine, which must not call t.Fatal.
func commitInErr(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("plan"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "plan.md"}, {"commit", "-qm", "plan"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %v: %s", args, err, out)
		}
	}
	return nil
}

// openPlannerSession opens a real planner session in work and keeps it under
// jobID, as startPlanner does.
func openPlannerSession(t *testing.T, work, jobID string) *planner.Session {
	t.Helper()
	s, err := planner.New(work)
	if err != nil {
		t.Fatalf("planner.New: %v", err)
	}
	policy := tools.Policy{Role: plannerRole, Permission: flowconfig.PermissionWorktreeWrite, Dir: s.Worktree}
	registerPlannerSession(jobID, plannerEntry{session: s, policy: policy})
	return s
}

func TestClosePlannerSessions_CleanRemovedKeptHasNotice(t *testing.T) {
	_, work := newPlanRepo(t)
	withTestWiring(t)
	resetPlannerSessions(t)

	clean := openPlannerSession(t, work, "job-clean")
	kept := openPlannerSession(t, work, "job-kept")
	commitIn(t, kept.Worktree)

	var out bytes.Buffer
	closePlannerSessions(&out)

	if _, err := os.Stat(clean.Worktree); !os.IsNotExist(err) {
		t.Fatalf("clean worktree %s was not removed (err = %v)", clean.Worktree, err)
	}
	if _, err := os.Stat(kept.Worktree); err != nil {
		t.Fatalf("kept worktree %s was removed: %v", kept.Worktree, err)
	}
	want := "planner session job-kept kept: its worktree is " + kept.Worktree
	if !strings.Contains(out.String(), want) {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
	if strings.Contains(out.String(), "job-clean") {
		t.Fatalf("output names the removed session: %q", out.String())
	}
	if n := len(plannerSessions); n != 0 {
		t.Fatalf("open planner sessions after exit = %d, want 0", n)
	}
}

func TestClosePlannerSessions_CancelsAndWaitsForResumedTurn(t *testing.T) {
	_, work := newPlanRepo(t)
	withTestWiring(t)
	resetPlannerSessions(t)

	s := openPlannerSession(t, work, "planner-job")
	stopped := make(chan struct{})
	var writeErr error
	// A resumed turn that runs until it is cancelled, then writes once more.
	// The write must land before Close decides whether the worktree is kept.
	job := JobRegistry.Start(context.Background(), "resumed turn", jobs.KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		defer close(stopped)
		<-ctx.Done()
		writeErr = commitInErr(s.Worktree)
		return "", false, ctx.Err()
	})
	resumableMu.Lock()
	resumable[job.ID] = resumableEntry{chain: []string{"planner-job", job.ID}}
	resumableMu.Unlock()
	t.Cleanup(func() {
		resumableMu.Lock()
		delete(resumable, job.ID)
		resumableMu.Unlock()
	})

	var out bytes.Buffer
	closePlannerSessions(&out)

	select {
	case <-stopped:
	default:
		t.Fatal("the resumed turn was not stopped before Close")
	}
	if writeErr != nil {
		t.Fatalf("write of the resumed turn: %v", writeErr)
	}
	if _, err := os.Stat(s.Worktree); err != nil {
		t.Fatalf("worktree removed under the resumed turn: %v", err)
	}
	if !strings.Contains(out.String(), "planner session planner-job kept") {
		t.Fatalf("output = %q, want the kept notice", out.String())
	}
}

func TestClosePlannerSessions_CloseErrorIsPrinted(t *testing.T) {
	_, work := newPlanRepo(t)
	withTestWiring(t)
	resetPlannerSessions(t)

	s := openPlannerSession(t, work, "job-broken")
	if err := os.RemoveAll(s.Worktree); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	closePlannerSessions(&out)
	if !strings.Contains(out.String(), "/plan: close planner session job-broken:") {
		t.Fatalf("output = %q, want the close error", out.String())
	}
}

func TestAgentRunWorkdir_ResumedPlannerRunsInItsWorktree(t *testing.T) {
	_, work := newPlanRepo(t)
	withTestWiring(t)
	resetPlannerSessions(t)

	s := openPlannerSession(t, work, "planner-job")
	resumableMu.Lock()
	resumable["resumed-job"] = resumableEntry{chain: []string{"planner-job", "resumed-job"}}
	resumableMu.Unlock()
	t.Cleanup(func() {
		resumableMu.Lock()
		delete(resumable, "resumed-job")
		resumableMu.Unlock()
	})

	for _, id := range []string{"planner-job", "resumed-job"} {
		got, err := agentRunWorkdir(id)
		if err != nil || got != s.Worktree {
			t.Fatalf("agentRunWorkdir(%s) = %q, %v; want %q", id, got, err, s.Worktree)
		}
	}

	ctx, err := agentResumeContext("resumed-job")
	if err != nil {
		t.Fatalf("agentResumeContext: %v", err)
	}
	if dir := tools.Workdir(ctx); dir != s.Worktree {
		t.Fatalf("resumed planner workdir = %q, want %q (never the process directory)", dir, s.Worktree)
	}
	p, ok := tools.PolicyOf(ctx)
	if !ok || p.Role != plannerRole || p.Permission != flowconfig.PermissionWorktreeWrite || p.Dir != s.Worktree {
		t.Fatalf("resumed planner policy = %+v, ok = %v", p, ok)
	}
}
