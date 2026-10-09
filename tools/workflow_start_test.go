package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// issueToMergeInfo is the workflow list of the tests below.
func issueToMergeInfo() []WorkflowInfo {
	return []WorkflowInfo{{
		Name:        "issue-to-merge",
		Description: "Merge one issue",
		Source:      "/repo/.tyci/workflows/issue-to-merge",
		Params: []WorkflowParam{
			{Name: "issue", Description: "GitHub issue number", Required: true},
			{Name: "branch", Description: "branch to merge into"},
		},
	}}
}

func TestWorkflowStart_RequiresName(t *testing.T) {
	f := &fakeWorkflowManager{list: issueToMergeInfo()}
	withWorkflowManager(t, f)
	res := RunTool(context.Background(), "workflow_start", map[string]any{})
	if res.Success || !res.validationError {
		t.Fatalf("start without workflow: %+v", res)
	}
	if want := "workflow is required. Available workflows: issue-to-merge issue [branch]: Merge one issue"; !strings.Contains(res.Error, want) {
		t.Fatalf("error = %q, want it to contain %q", res.Error, want)
	}
	if f.workflow != "" {
		t.Fatalf("manager called with workflow %q", f.workflow)
	}
}

func TestWorkflowStart_UnknownNameListsWorkflows(t *testing.T) {
	f := &fakeWorkflowManager{list: issueToMergeInfo(), err: errors.New(`workflow "nope" not found`)}
	withWorkflowManager(t, f)
	res := RunTool(context.Background(), "workflow_start", map[string]any{"workflow": "nope"})
	if res.Success {
		t.Fatalf("unknown workflow succeeded: %+v", res)
	}
	if want := "Available workflows: issue-to-merge issue [branch]: Merge one issue"; !strings.Contains(res.Error, want) {
		t.Fatalf("error = %q, want it to contain %q", res.Error, want)
	}
}

func TestWorkflowStart_UnknownNameKeepsManagerList(t *testing.T) {
	f := &fakeWorkflowManager{list: issueToMergeInfo(), err: errors.New(`workflow "nope" not found (available: issue-to-merge)`)}
	withWorkflowManager(t, f)
	res := RunTool(context.Background(), "workflow_start", map[string]any{"workflow": "nope"})
	if strings.Count(res.Error, "available") != 1 {
		t.Fatalf("the list is repeated: %q", res.Error)
	}
}

func TestWorkflowStart_BindsParams(t *testing.T) {
	f := &fakeWorkflowManager{}
	withWorkflowManager(t, f)
	// A JSON number and a string both reach the manager as text.
	res := RunTool(context.Background(), "workflow_start", map[string]any{"workflow": "issue-to-merge", "params": []any{float64(160)}})
	if !res.Success || len(f.params) != 1 || f.params[0] != "160" {
		t.Fatalf("number param: %+v params=%q", res, f.params)
	}
	res = RunTool(context.Background(), "workflow_start", map[string]any{"workflow": "issue-to-merge", "params": []string{"160", "dev"}})
	if !res.Success || len(f.params) != 2 || f.params[0] != "160" || f.params[1] != "dev" {
		t.Fatalf("string params: %+v params=%q", res, f.params)
	}
	if res := RunTool(context.Background(), "workflow_start", map[string]any{"workflow": "issue-to-merge", "params": "160"}); res.Success || !res.validationError {
		t.Fatalf("params as a string: %+v", res)
	}
}

func TestWorkflowStart_MissingParamText(t *testing.T) {
	want := "missing required param issue (GitHub issue number): ask the user, then call workflow_start again"
	f := &fakeWorkflowManager{list: issueToMergeInfo(), err: errors.New(want)}
	withWorkflowManager(t, f)
	res := RunTool(context.Background(), "workflow_start", map[string]any{"workflow": "issue-to-merge"})
	if res.Success || res.Error != want {
		t.Fatalf("error = %q, want %q", res.Error, want)
	}
}

func TestWorkflowStart_OldIssueKeyRefused(t *testing.T) {
	f := &fakeWorkflowManager{}
	withWorkflowManager(t, f)
	res := RunTool(context.Background(), "workflow_start", map[string]any{"workflow": "issue-to-merge", "issue": float64(160)})
	if res.Success || !strings.Contains(res.Error, "use params instead") {
		t.Fatalf("old issue key: %+v", res)
	}
	if f.workflow != "" {
		t.Fatal("manager was called for the old issue key")
	}
}

func TestWorkflowToolsSchema_ListsWorkflows(t *testing.T) {
	schema := workflowToolsSchema(issueToMergeInfo())
	var start map[string]any
	for _, s := range schema {
		if fn := s["function"].(map[string]any); fn["name"] == "workflow_start" {
			start = fn
		}
	}
	if start == nil {
		t.Fatal("no workflow_start in schema")
	}
	desc, _ := start["description"].(string)
	if want := "issue-to-merge issue [branch]: Merge one issue"; !strings.Contains(desc, want) {
		t.Fatalf("description = %q, want it to contain %q", desc, want)
	}
	params := start["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)
	if _, ok := props["issue"]; ok {
		t.Fatal("schema still has the issue property")
	}
	p := props["params"].(map[string]any)
	if p["type"] != "array" || p["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("params property = %v", p)
	}
	if req := params["required"].([]string); len(req) != 1 || req[0] != "workflow" {
		t.Fatalf("required = %v", req)
	}
}

func TestWorkflowToolsSchema_NoWorkflows(t *testing.T) {
	desc, _ := json.Marshal(workflowToolsSchema(nil))
	if !strings.Contains(string(desc), "No workflows are available.") {
		t.Fatalf("schema = %s", desc)
	}
}

func TestWorkflowToolsSchema_DynamicFromManager(t *testing.T) {
	withWorkflowManager(t, &fakeWorkflowManager{list: issueToMergeInfo()})
	data, err := json.Marshal(GetToolsSchema())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "issue-to-merge issue [branch]: Merge one issue") {
		t.Fatal("chat schema does not list the workflows of the manager")
	}
}
