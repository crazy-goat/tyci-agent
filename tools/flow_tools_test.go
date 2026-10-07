package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeWorkflowManager struct {
	issue  int
	answer string
	err    error
}

func (f *fakeWorkflowManager) Start(_ context.Context, _ string, issue int) (string, []string, error) {
	f.issue = issue
	return "20261005-120301-160", nil, f.err
}
func (f *fakeWorkflowManager) Status(string) (any, error) {
	return map[string]any{"status": "running"}, f.err
}
func (f *fakeWorkflowManager) Resume(_, answer string) error { f.answer = answer; return f.err }

func withWorkflowManager(t *testing.T, m WorkflowManager) {
	t.Helper()
	SetWorkflowManager(m)
	t.Cleanup(func() { SetWorkflowManager(nil) })
}

func TestWorkflowTools_StartStatusResume(t *testing.T) {
	f := &fakeWorkflowManager{}
	withWorkflowManager(t, f)
	res := RunTool(context.Background(), "workflow_start", map[string]any{"issue": float64(160)})
	if !res.Success || f.issue != 160 || !strings.Contains(res.Content, `"run":"20261005-120301-160"`) || !strings.Contains(res.Content, `"warnings":[]`) {
		t.Fatalf("start: %+v", res)
	}
	if res := RunTool(context.Background(), "workflow_start", map[string]any{}); res.Success {
		t.Fatal("start without issue succeeded")
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
	names := []string{"workflow_start", "workflow_status", "workflow_resume"}
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
