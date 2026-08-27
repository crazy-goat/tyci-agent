package workflow

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/tools"
)

// fakeSubAgentRunner is a minimal tools.SubAgentRunner that records every
// task it was given and returns a fixed reply per task — enough to prove
// tyci.subagent() actually reaches a real child-agent runner, without
// needing the full jobs.Registry wiring main.go does in production.
// runTasks (tools/subagent.go) runs each task on its own goroutine, so
// gotTasks is mutex-guarded.
type fakeSubAgentRunner struct {
	mu       sync.Mutex
	gotTasks []string
}

func (r *fakeSubAgentRunner) RunTask(ctx context.Context, task string, model string, opts tools.SubagentOptions) (string, error) {
	return r.RunTaskWithSystem(ctx, task, model, "", opts)
}

func (r *fakeSubAgentRunner) RunTaskWithSystem(ctx context.Context, task string, model string, system string, opts tools.SubagentOptions) (string, error) {
	r.mu.Lock()
	r.gotTasks = append(r.gotTasks, task)
	r.mu.Unlock()
	return "child result for: " + task, nil
}

// tasks returns a sorted snapshot of every task RunTaskWithSystem recorded.
func (r *fakeSubAgentRunner) tasks() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.gotTasks...)
	sort.Strings(out)
	return out
}

// TestLuaSubagent_SpawnsChildAgent verifies tyci.subagent(...) sugar (added
// so a script doesn't have to hand-roll tyci.run_tool("subagent", ...))
// actually dispatches to a real subagent runner rather than merely building
// arguments.
func TestLuaSubagent_SpawnsChildAgent(t *testing.T) {
	runner := &fakeSubAgentRunner{}
	tools.SetSubAgentRunner(runner)
	t.Cleanup(func() { tools.SetSubAgentRunner(nil) })

	// The subagent tool reads its default model off a connector.ModelClient
	// stashed in ctx (see tools/subagent.go's SubagentTool.Run) — a fake
	// client is enough since fakeSubAgentRunner never calls it itself.
	fakeClient := &connectortest.Fake{ProviderName: "wf-fake", ModelName: "wf-fake-1"}
	ctx := connector.WithModelClient(context.Background(), fakeClient)

	engine := NewEngine(ctx, "prompt")
	argsTbl := engine.L.NewTable()
	engine.L.SetField(argsTbl, "task", lua.LString("summarize the repo"))

	engine.L.Push(engine.L.NewFunction(engine.luaSubagent))
	engine.L.Push(argsTbl)
	if err := engine.L.PCall(1, 1, nil); err != nil {
		t.Fatalf("tyci.subagent failed: %v", err)
	}
	result := engine.L.Get(-1)
	engine.L.Pop(1)

	resultTbl, ok := result.(*lua.LTable)
	if !ok {
		t.Fatalf("tyci.subagent must push a table, got %T", result)
	}
	if success := engine.L.GetField(resultTbl, "success"); success != lua.LTrue {
		t.Fatalf("tyci.subagent success = %v, want true (error: %v)", success, engine.L.GetField(resultTbl, "error"))
	}
	wantContent := "child result for: summarize the repo"
	if got := engine.L.GetField(resultTbl, "content").String(); got != wantContent {
		t.Errorf("tyci.subagent content = %q, want %q", got, wantContent)
	}
	if got := runner.tasks(); len(got) != 1 || got[0] != "summarize the repo" {
		t.Errorf("subagent runner received tasks %v, want [%q]", got, "summarize the repo")
	}
}

// TestLuaSubagent_FanOutTasksArray is the regression test for the fix to
// convertLuaValueToGo: a Lua array table ({...} with integer keys 1..n) used
// to convert to map[string]any{} because the old conversion only kept
// string-keyed entries, so tyci.subagent({tasks = {...}}) arrived at
// tools/subagent.go's parseTasks as an object and was rejected with "tasks
// must be an array" — the fan-out sugar item 7 asked for never actually
// worked. This drives tyci.subagent with two tasks and checks both actually
// ran.
func TestLuaSubagent_FanOutTasksArray(t *testing.T) {
	runner := &fakeSubAgentRunner{}
	tools.SetSubAgentRunner(runner)
	t.Cleanup(func() { tools.SetSubAgentRunner(nil) })

	fakeClient := &connectortest.Fake{ProviderName: "wf-fake", ModelName: "wf-fake-1"}
	ctx := connector.WithModelClient(context.Background(), fakeClient)

	engine := NewEngine(ctx, "prompt")

	task1 := engine.L.NewTable()
	engine.L.SetField(task1, "task", lua.LString("task one"))
	task2 := engine.L.NewTable()
	engine.L.SetField(task2, "task", lua.LString("task two"))
	tasksArr := engine.L.NewTable()
	tasksArr.Append(task1)
	tasksArr.Append(task2)
	argsTbl := engine.L.NewTable()
	engine.L.SetField(argsTbl, "tasks", tasksArr)

	engine.L.Push(engine.L.NewFunction(engine.luaSubagent))
	engine.L.Push(argsTbl)
	if err := engine.L.PCall(1, 1, nil); err != nil {
		t.Fatalf("tyci.subagent failed: %v", err)
	}
	result := engine.L.Get(-1)
	engine.L.Pop(1)

	resultTbl, ok := result.(*lua.LTable)
	if !ok {
		t.Fatalf("tyci.subagent must push a table, got %T", result)
	}
	if success := engine.L.GetField(resultTbl, "success"); success != lua.LTrue {
		t.Fatalf("tyci.subagent success = %v, want true (error: %v, content: %v)",
			success, engine.L.GetField(resultTbl, "error"), engine.L.GetField(resultTbl, "content"))
	}
	content := engine.L.GetField(resultTbl, "content").String()
	if !strings.Contains(content, "task one") || !strings.Contains(content, "task two") {
		t.Errorf("tyci.subagent content = %q, want it to mention both child results", content)
	}

	got := runner.tasks()
	want := []string{"task one", "task two"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("subagent runner received tasks %v, want %v", got, want)
	}
}

// fakeJobWaiter satisfies tools.JobWaiter (wait.go) by returning a fixed,
// already-finished status for one known job id.
type fakeJobWaiter struct {
	id     string
	status tools.JobStatus
}

func (w *fakeJobWaiter) Wait(ctx context.Context, id string, timeout time.Duration) (tools.JobStatus, bool) {
	if id != w.id {
		return tools.JobStatus{}, false
	}
	return w.status, true
}

// TestLuaWait_WaitsOnJob verifies tyci.wait(job_id) sugar reaches the real
// "wait" tool and returns the job's actual result, for both call shapes:
// a bare job_id string and an {job_id=...} options table.
func TestLuaWait_WaitsOnJob(t *testing.T) {
	waiter := &fakeJobWaiter{
		id: "job-42",
		status: tools.JobStatus{
			ID: "job-42", Done: true, Success: true, Content: "finished the analysis",
		},
	}
	tools.SetJobWaiter(waiter)
	t.Cleanup(func() { tools.SetJobWaiter(nil) })

	engine := NewEngine(context.Background(), "prompt")

	// Bare string form: tyci.wait("job-42")
	engine.L.Push(engine.L.NewFunction(engine.luaWait))
	engine.L.Push(lua.LString("job-42"))
	if err := engine.L.PCall(1, 1, nil); err != nil {
		t.Fatalf("tyci.wait(job_id) failed: %v", err)
	}
	result := engine.L.Get(-1)
	engine.L.Pop(1)
	resultTbl, ok := result.(*lua.LTable)
	if !ok {
		t.Fatalf("tyci.wait must push a table, got %T", result)
	}
	if got := engine.L.GetField(resultTbl, "content").String(); !strings.Contains(got, "finished the analysis") {
		t.Errorf("tyci.wait(job_id) content = %q, want it to contain %q", got, "finished the analysis")
	}

	// Table form: tyci.wait({job_id = "job-42"})
	optsTbl := engine.L.NewTable()
	engine.L.SetField(optsTbl, "job_id", lua.LString("job-42"))
	engine.L.Push(engine.L.NewFunction(engine.luaWait))
	engine.L.Push(optsTbl)
	if err := engine.L.PCall(1, 1, nil); err != nil {
		t.Fatalf("tyci.wait({job_id=...}) failed: %v", err)
	}
	result2 := engine.L.Get(-1)
	engine.L.Pop(1)
	result2Tbl, ok := result2.(*lua.LTable)
	if !ok {
		t.Fatalf("tyci.wait must push a table, got %T", result2)
	}
	if got := engine.L.GetField(result2Tbl, "content").String(); !strings.Contains(got, "finished the analysis") {
		t.Errorf("tyci.wait({job_id=...}) content = %q, want it to contain %q", got, "finished the analysis")
	}
}
