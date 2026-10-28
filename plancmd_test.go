package main

import (
	"bytes"
	"context"
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

	"github.com/crazy-goat/tyci-agent/agent"
	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/display"
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/hooks"
	"github.com/crazy-goat/tyci-agent/internal/planner"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/stream"
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

// TestPlanCommand_PlannerWritesOnlyInPlanWorktree runs the write tool with the
// policy that startPlanner builds. A write, or an edit (write with oldString),
// outside the plan worktree is refused, and a write inside it works. The
// context is built the way tools.runSubagentTask builds it: the policy and
// the working directory of the job.
func TestPlanCommand_PlannerWritesOnlyInPlanWorktree(t *testing.T) {
	home, work := newPlanRepo(t)
	withTestWiring(t)
	// Hooks are process-wide. A hook that an earlier test left installed
	// would refuse the write before the write root is checked.
	t.Cleanup(hooks.SetForTesting(nil))
	f := installPlanFakes(t, home, work)

	if err := startPlanner(context.Background(), "plan the cache"); err != nil {
		t.Fatalf("startPlanner: %v", err)
	}
	nextNotice(t, f)
	f.finish(t)
	spec := f.specs[0]
	if spec.Policy == nil {
		t.Fatal("planner job has no policy")
	}
	ctx := tools.WithPolicy(tools.WithWorkdir(context.Background(), spec.Dir), *spec.Policy)

	outside := filepath.Join(work, "outside.md")
	res := tools.RunTool(ctx, "write", map[string]any{"path": outside, "content": "x"})
	if res.Success || !strings.Contains(res.Error, "outside the worktree") {
		t.Fatalf("write outside the plan worktree: success = %v, error = %q", res.Success, res.Error)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("file outside the plan worktree exists (err = %v)", err)
	}

	existing := filepath.Join(work, "x")
	res = tools.RunTool(ctx, "write", map[string]any{"path": existing, "oldString": "x", "newString": "y"})
	if res.Success || !strings.Contains(res.Error, "outside the worktree") {
		t.Fatalf("edit outside the plan worktree: success = %v, error = %q", res.Success, res.Error)
	}
	if got, err := os.ReadFile(existing); err != nil || string(got) != "x" {
		t.Fatalf("file in the main checkout changed: %q, %v", got, err)
	}

	inside := filepath.Join(spec.Dir, "plan.md")
	res = tools.RunTool(ctx, "write", map[string]any{"path": inside, "content": "plan"})
	if !res.Success {
		t.Fatalf("write inside the plan worktree refused: %s", res.Error)
	}
	if got, err := os.ReadFile(inside); err != nil || string(got) != "plan" {
		t.Fatalf("plan file = %q, %v; want plan", got, err)
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
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	planGit(t, dir, "add", "plan.md")
	planGit(t, dir, "commit", "-qm", "plan")
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

// resumePlannerTurn stashes the first turn of the planner root, as its run
// does when it ends, and resumes it through the agent view. It returns the id
// of the new turn. The turn is cancelled at the end of the test, if it runs.
func resumePlannerTurn(t *testing.T, root string, fake *connectortest.Fake, text string) string {
	t.Helper()
	stashResumable(root, resumableEntry{
		msgs: []connector.Message{
			{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "first"}}},
		},
		mc:          fake,
		cfg:         agent.Config{MaxRetries: 1},
		todoAgentID: root,
	})
	id, err := agentViewInput{}.Resume(root, text)
	if err != nil {
		t.Fatalf("resume %s: %v", root, err)
	}
	t.Cleanup(func() { JobRegistry.Cancel(id) })
	return id
}

// waitForCalls waits until the fake model got its first request, so the turn
// is live.
func waitForCalls(t *testing.T, fake *connectortest.Fake) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for fake.Calls() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the resumed turn never called the model")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestClosePlannerSessions_CancelsAndWaitsForResumedTurn(t *testing.T) {
	_, work := newPlanRepo(t)
	withTestWiring(t)
	resetPlannerSessions(t)
	resetResumableForTest(t)
	s := openPlannerSession(t, work, "planner-job")

	// The turn blocks until it is cancelled, so it is still running at exit.
	fake := &connectortest.Fake{BlockUntilCancel: true}
	id := resumePlannerTurn(t, "planner-job", fake, "more")
	waitForCalls(t, fake)

	var out bytes.Buffer
	closePlannerSessions(&out)

	if job, ok := JobRegistry.Get(id); !ok || job.Status == jobs.StatusRunning {
		t.Fatalf("resumed turn %s still runs after its session was closed", id)
	}
	if _, err := os.Stat(s.Worktree); !os.IsNotExist(err) {
		t.Fatalf("clean worktree %s was not removed after the turn stopped (err = %v)", s.Worktree, err)
	}
}

func TestResumedPlannerTurn_RunsInItsWorktreeAndChain(t *testing.T) {
	_, work := newPlanRepo(t)
	withTestWiring(t)
	resetPlannerSessions(t)
	resetResumableForTest(t)
	s := openPlannerSession(t, work, "planner-job")

	fake := connectortest.Text("answer")
	first := resumePlannerTurn(t, "planner-job", fake, "more")
	if _, ok := JobRegistry.Wait(context.Background(), first, 10*time.Second); !ok {
		t.Fatal("the resumed turn never ended")
	}
	for _, id := range []string{"planner-job", first} {
		got, err := agentRunWorkdir(id)
		if err != nil || got != s.Worktree {
			t.Fatalf("agentRunWorkdir(%s) = %q, %v; want %q", id, got, err, s.Worktree)
		}
	}
}

// TestResumeTool_PlannerTurnRunsInItsWorktree checks the chat tool "resume":
// it calls the same resumer as the agent view, with no worktree in its
// context. A planner turn it starts must write in the planner worktree.
func TestResumeTool_PlannerTurnRunsInItsWorktree(t *testing.T) {
	_, work := newPlanRepo(t)
	reg, _ := withTestWiring(t)
	resetPlannerSessions(t)
	resetResumableForTest(t)
	s := openPlannerSession(t, work, "planner-job")
	// The hook registry is process-wide. A test that loads global hooks leaves
	// them set, and a pre_tool hook would block the write. So none run here.
	t.Cleanup(hooks.SetForTesting(nil))
	tools.SetJobResumer(jobResumerAdapter{reg: reg})
	t.Cleanup(func() { tools.SetJobResumer(nil) })

	const file = "tool-resume-note.txt"
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(filepath.Join(cwd, file)) })

	const task = "write the note"
	fake := &connectortest.Fake{Script: func(turn int, _ connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.ToolCall{ID: "tc-write", Name: "write", Arguments: `{"path":"` + file + `","content":"note"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		return []stream.Event{stream.TextDelta{Text: "done"}, stream.Finish{Reason: "stop"}}
	}}
	stashResumable("planner-job", resumableEntry{
		msgs: []connector.Message{
			{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "first"}}},
		},
		mc:          fake,
		cfg:         agent.Config{MaxRetries: 1, Tools: &subagentToolRunner{}},
		todoAgentID: "planner-job",
	})

	res := (&tools.ResumeTool{}).Run(context.Background(), map[string]any{"job_id": "planner-job", "task": task})
	if !res.Success {
		t.Fatalf("resume tool: %+v", res)
	}
	var id string
	for _, j := range reg.List() {
		if j.Description == task {
			id = j.ID
		}
	}
	if id == "" {
		t.Fatal("the resume tool started no turn")
	}
	if _, ok := reg.Wait(context.Background(), id, 10*time.Second); !ok {
		t.Fatal("the resumed turn never ended")
	}
	if _, err := os.Stat(filepath.Join(s.Worktree, file)); err != nil {
		t.Fatalf("the tool resume did not write in the planner worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, file)); err == nil {
		t.Fatal("the tool resume wrote in the process directory")
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
