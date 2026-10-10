package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeWorkflowManager struct {
	workflow string
	params   []string
	answer   string
	err      error
	list     []WorkflowInfo
	// stopped and stopReason record the last Stop call.
	stopped, stopReason string
	// listArchived, listLimit and listOffset record the last List call.
	listArchived          bool
	listLimit, listOffset int
}

func (f *fakeWorkflowManager) Start(_ context.Context, workflow string, params []string) (string, []string, error) {
	f.workflow, f.params = workflow, params
	return "20261005-120301-160", nil, f.err
}
func (f *fakeWorkflowManager) Workflows() []WorkflowInfo { return f.list }
func (f *fakeWorkflowManager) Status(string) (any, error) {
	return map[string]any{"status": "running"}, f.err
}
func (f *fakeWorkflowManager) List(archived bool, limit, offset int) (any, error) {
	f.listArchived, f.listLimit, f.listOffset = archived, limit, offset
	return map[string]any{"runs": []any{}, "total": 0}, f.err
}
func (f *fakeWorkflowManager) Resume(_, answer string) error { f.answer = answer; return f.err }
func (f *fakeWorkflowManager) Stop(run, reason string) (any, error) {
	f.stopped, f.stopReason = run, reason
	return map[string]any{"run": run, "status": "stopped"}, f.err
}

func withWorkflowManager(t *testing.T, m WorkflowManager) {
	t.Helper()
	SetWorkflowManager(m)
	t.Cleanup(func() { SetWorkflowManager(nil) })
}

func TestWorkflowTools_StartStatusResume(t *testing.T) {
	f := &fakeWorkflowManager{}
	withWorkflowManager(t, f)
	res := RunTool(context.Background(), "workflow_start", map[string]any{"workflow": "issue-to-merge", "params": []any{"160"}})
	if !res.Success || f.workflow != "issue-to-merge" || len(f.params) != 1 || f.params[0] != "160" || !strings.Contains(res.Content, `"run":"20261005-120301-160"`) || !strings.Contains(res.Content, `"warnings":[]`) {
		t.Fatalf("start: %+v", res)
	}
	if res := RunTool(context.Background(), "workflow_start", map[string]any{"params": []any{"160"}}); res.Success {
		t.Fatal("start without workflow succeeded")
	}
	if res := RunTool(context.Background(), "workflow_status", nil); !res.Success || !strings.Contains(res.Content, "running") {
		t.Fatalf("status: %+v", res)
	}
	if res := RunTool(context.Background(), "workflow_resume", map[string]any{"run": "r", "answer": "retry"}); !res.Success || f.answer != "retry" {
		t.Fatalf("resume: %+v", res)
	}
	f.err = errors.New("unknown answer \"x\", allowed: retry, stop")
	if res := RunTool(context.Background(), "workflow_resume", map[string]any{"run": "r", "answer": "x"}); res.Success || !strings.Contains(res.Error, "retry, stop") {
		t.Fatalf("bad answer: %+v", res)
	}
}

func TestSubagentToolSetExcludesWorkflowTools(t *testing.T) {
	withWorkflowManager(t, &fakeWorkflowManager{})
	names := []string{"workflow_start", "workflow_status", "workflow_list", "workflow_resume", "workflow_stop"}
	for _, schema := range []string{string(GetSubagentToolsSchemaJSON()), string(GetSubagentToolsSchemaJSONFor(names))} {
		for _, n := range names {
			if strings.Contains(schema, `"`+n+`"`) {
				t.Errorf("subagent schema offers %s", n)
			}
		}
	}
	if !strings.Contains(string(GetToolsSchemaJSON())+string(GetAllToolsSchemaJSON()), `"workflow_start"`) {
		t.Error("chat schema lacks workflow_start")
	}
	if !IsSubagentDenied("workflow_stop") {
		t.Error("workflow_stop is not denied to subagents")
	}
	gate := AllowOnlySubagent(names)
	for _, n := range names {
		if gate(n) == nil {
			t.Errorf("gate allows %s", n)
		}
	}
}

func TestWorkflowToolsAbsentFromSchemaWithoutManager(t *testing.T) {
	SetWorkflowManager(nil)
	if strings.Contains(string(GetToolsSchemaJSON())+string(GetAllToolsSchemaJSON()), `"workflow_start"`) {
		t.Error("schema offers workflow_start without a manager")
	}
}

func TestTopLevelSchemaJSONHasWorkflowToolsOnceManagerSet(t *testing.T) {
	SetWorkflowManager(nil)
	if strings.Contains(string(GetTopLevelToolsSchemaJSON()), `"workflow_start"`) {
		t.Fatal("workflow_start must be absent before the manager is set")
	}
	withWorkflowManager(t, &fakeWorkflowManager{})
	if !strings.Contains(string(GetTopLevelToolsSchemaJSON()), `"workflow_start"`) {
		t.Fatal("workflow_start missing from the top-level schema after the manager is set")
	}
}

// Strict providers (nexos) reject a tool schema with "required": null.
func TestTopLevelSchemaJSONHasNoNullRequired(t *testing.T) {
	withWorkflowManager(t, &fakeWorkflowManager{})
	if strings.Contains(string(GetTopLevelToolsSchemaJSON()), `"required":null`) {
		t.Fatal(`top-level schema contains "required":null`)
	}
}

func TestWorkflowStop_ReturnsStoppedState(t *testing.T) {
	f := &fakeWorkflowManager{}
	withWorkflowManager(t, f)
	res := RunTool(context.Background(), "workflow_stop", map[string]any{"run": "r1", "reason": "too slow"})
	if !res.Success || !strings.Contains(res.Content, `"status":"stopped"`) {
		t.Fatalf("stop: %+v", res)
	}
	if f.stopped != "r1" || f.stopReason != "too slow" {
		t.Fatalf("manager got run %q reason %q", f.stopped, f.stopReason)
	}
}

func TestWorkflowStop_RequiresRun(t *testing.T) {
	withWorkflowManager(t, &fakeWorkflowManager{})
	res := RunTool(context.Background(), "workflow_stop", map[string]any{"reason": "x"})
	if res.Success || !res.validationError {
		t.Fatalf("stop without run: %+v", res)
	}
}

func TestWorkflowStop_ErrorIsReturned(t *testing.T) {
	f := &fakeWorkflowManager{err: errors.New("run \"x\" is not active; active runs: a, b")}
	withWorkflowManager(t, f)
	res := RunTool(context.Background(), "workflow_stop", map[string]any{"run": "x"})
	if res.Success || !strings.Contains(res.Error, "active runs") {
		t.Fatalf("stop error: %+v", res)
	}
}

func TestWorkflowListTool_PassesFiltersAndPage(t *testing.T) {
	f := &fakeWorkflowManager{}
	withWorkflowManager(t, f)
	res := RunTool(context.Background(), "workflow_list", map[string]any{"archived": true, "limit": float64(5), "offset": float64(10)})
	if !res.Success || !strings.Contains(res.Content, `"total":0`) {
		t.Fatalf("list: %+v", res)
	}
	if !f.listArchived || f.listLimit != 5 || f.listOffset != 10 {
		t.Fatalf("list args = %v/%d/%d", f.listArchived, f.listLimit, f.listOffset)
	}
	if res := RunTool(context.Background(), "workflow_list", nil); !res.Success || f.listArchived || f.listLimit != 0 || f.listOffset != 0 {
		t.Fatalf("list without args: %+v args %v/%d/%d", res, f.listArchived, f.listLimit, f.listOffset)
	}
}

func TestWorkflowListTool_ErrorFromManager(t *testing.T) {
	withWorkflowManager(t, &fakeWorkflowManager{err: errors.New("limit must be a positive number")})
	if res := RunTool(context.Background(), "workflow_list", map[string]any{"limit": float64(-1)}); res.Success || !strings.Contains(res.Error, "positive") {
		t.Fatalf("list error: %+v", res)
	}
}

func TestWorkflowListTool_DeniedToSubagents(t *testing.T) {
	if !IsSubagentDenied("workflow_list") {
		t.Error("workflow_list is not denied to subagents")
	}
}

func TestWorkflowStopSchema_InTopLevelSchemaOnce(t *testing.T) {
	withWorkflowManager(t, &fakeWorkflowManager{})
	if n := strings.Count(string(GetTopLevelToolsSchemaJSON()), `"name":"workflow_stop"`); n != 1 {
		t.Fatalf("workflow_stop appears %d times in the top-level schema", n)
	}
}
