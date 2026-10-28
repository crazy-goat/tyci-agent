package flow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/tools"
)

const flowMadeSource = `return {
  schema = {name = "flow_made", description = "test", parameters = {}},
  run = function(ctx, args) return {success = true, content = "ok"} end
}`

// A run that ends removes the Lua tools made in its run folder. A paused run keeps them.
func TestEndRunToolsRemovesToolsOfEndedRun(t *testing.T) {
	defer tools.SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	ctx := tools.WithRunDir(context.Background(), dir)
	if res := tools.RunTool(ctx, "tool_create", map[string]any{"name": "flow_made", "source": flowMadeSource}); !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}

	endRunTools("paused", dir)
	if res := tools.RunTool(context.Background(), "flow_made", nil); !res.Success {
		t.Fatal("a paused run lost its tool")
	}

	endRunTools("done", dir)
	if res := tools.RunTool(context.Background(), "flow_made", nil); res.Success {
		t.Fatal("the tool of the ended run is still registered")
	}
}

// The role context of an agent carries the run folder, so tool_create writes into it.
func TestText_SetsRunDirForTheAgent(t *testing.T) {
	var got string
	r := newRunner(&spawnRec{})
	r.Spawn = func(ctx context.Context, _ tools.TaskSpec) (string, string, error) {
		got = tools.RunDirFrom(ctx)
		return "ok", "sess", nil
	}
	dir := t.TempDir()
	if _, _, err := r.Text(context.Background(), "worker", "", RunContext{RunDir: dir, Worktree: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("run dir in the agent context = %q, want %q", got, dir)
	}
}

// launchRun starts st through Manager.launch, as Start and Resume do, and returns
// the last event of the run. The event comes after the run ends its tools.
func launchRun(t *testing.T, m *Manager, info RepoInfo, wf *Workflow, st *RunState, resumed bool, do func(context.Context, *Runner) error) RunEvent {
	t.Helper()
	ended := make(chan RunEvent, 1)
	unsub := m.Subscribe(func(ev RunEvent) {
		if ev.Run == st.Run && ev.Status != "running" {
			select {
			case ended <- ev:
			default:
			}
		}
	})
	defer unsub()
	m.mu.Lock()
	m.launch(info, wf, st, resumed, do)
	m.mu.Unlock()
	select {
	case ev := <-ended:
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not end")
		return RunEvent{}
	}
}

// toolsWorkflow is one agent state that ends the run with "done".
func toolsWorkflow() *Workflow {
	return &Workflow{Name: "tools", Start: "a", States: map[string]State{
		"a":   {Agent: "worker", On: map[string]string{"done": "end"}},
		"end": {End: true},
	}}
}

// toolsRun is a run that starts at the agent state of toolsWorkflow.
func toolsRun(id string) *RunState {
	return &RunState{Version: 1, Run: id, Workflow: "tools", Repo: "o/r",
		Status: "running", Current: "a", Visits: map[string]int{}, History: []Step{}}
}

// A tool that the agent of a run creates with tool_create is gone when the run
// ends. The test covers the run folder in the agent context (agent.go) and the
// removal at the run end (manager.go launch).
func TestRunEndRemovesToolMadeByAgent(t *testing.T) {
	defer tools.SnapshotLuaToolsForTesting()()
	info := RepoInfo{Home: t.TempDir(), Root: t.TempDir(), Repo: "o/r", DefaultBranch: "main"}
	st := toolsRun("run1")
	runDir := RunDir(info.Home, info.Name(), st.Run)
	wf := toolsWorkflow()
	// The first spawn is the agent; the report reminder that follows resumes its session.
	spawn := func(ctx context.Context, sp tools.TaskSpec) (string, string, error) {
		if sp.Resume == "" {
			res := tools.RunTool(ctx, "tool_create", map[string]any{"name": "flow_made", "source": flowMadeSource})
			if !res.Success {
				return "", "", errors.New(res.Error)
			}
			// The worker must write its report into the artifact folder of its step.
			if err := os.WriteFile(filepath.Join(runDir, "artifacts", "001-a", "report.md"), []byte("done\n"), 0o600); err != nil {
				return "", "", err
			}
		}
		return "ok", "sess", nil
	}
	m := &Manager{NewRunner: func(RepoInfo, *Workflow, *RunState) *Runner {
		agents := NewSubagentRunner(testCfg(), spawn)
		agents.Render = fakeRender{}
		return &Runner{WF: wf, Agents: agents, Store: &Store{Dir: runDir}, RunDir: runDir}
	}}

	ev := launchRun(t, m, info, wf, st, false, func(ctx context.Context, r *Runner) error { return r.Run(ctx, st) })
	if ev.Status != "done" {
		t.Fatalf("run status = %s (%s), want done: the tool_create in the agent failed", ev.Status, ev.Reason)
	}
	if res := tools.RunTool(context.Background(), "flow_made", nil); res.Success {
		t.Fatal("the tool made by the agent is still registered after the run ended")
	}
}

// A paused run keeps its tool files. When it starts again, the tools are
// registered before the agent runs, and a bad file does not stop the start.
// The run end removes them.
func TestRunStartReloadsToolsOfRunFolder(t *testing.T) {
	defer tools.SnapshotLuaToolsForTesting()()
	info := RepoInfo{Home: t.TempDir(), Root: t.TempDir(), Repo: "o/r", DefaultBranch: "main"}
	st := toolsRun("run2")
	runDir := RunDir(info.Home, info.Name(), st.Run)
	if err := os.MkdirAll(filepath.Join(runDir, "tools"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "tools", "flow_made.lua"), []byte(flowMadeSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "tools", "broken.lua"), []byte("return {"), 0o600); err != nil {
		t.Fatal(err)
	}
	wf := toolsWorkflow()
	m := &Manager{NewRunner: func(RepoInfo, *Workflow, *RunState) *Runner { return &Runner{WF: wf, Store: &Store{Dir: runDir}} }}

	var seen bool
	ev := launchRun(t, m, info, wf, st, true, func(ctx context.Context, _ *Runner) error {
		seen = tools.RunTool(ctx, "flow_made", nil).Success
		st.Status = "done"
		return nil
	})
	if !seen {
		t.Fatal("the tool of the run folder was not registered when the run started")
	}
	if ev.Status != "done" {
		t.Fatalf("run status = %s", ev.Status)
	}
	if res := tools.RunTool(context.Background(), "flow_made", nil); res.Success {
		t.Fatal("the reloaded tool is still registered after the run ended")
	}
}
