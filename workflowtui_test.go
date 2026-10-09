package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/display"
	"github.com/crazy-goat/tyci-agent/internal/flow"
)

// testFlowManager is a manager that never touches git: Info is fixed and the
// workflow is given by the test.
func testFlowManager(info flow.RepoInfo, wf *flow.Workflow) *flow.Manager {
	return &flow.Manager{
		Info: func() (flow.RepoInfo, error) { return info, nil },
		Workflow: func(flow.RepoInfo, string) (*flow.Workflow, error) {
			return wf, nil
		},
	}
}

func writeTestWorkflow(t *testing.T, home, name, json string) {
	t.Helper()
	dir := filepath.Join(home, ".tyci", "workflows", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFlowWorkflowsStartReportsMissingParam(t *testing.T) {
	wf := &flow.Workflow{Name: "issue-to-merge", Params: []flow.Param{{Name: "issue", Description: "Issue number", Required: true}}}
	w := flowWorkflows{M: testFlowManager(flow.RepoInfo{}, wf)}
	_, _, err := w.Start("issue-to-merge", nil)
	var pe display.WorkflowParamError
	if !errors.As(err, &pe) || pe.Name != "issue" || pe.Description != "Issue number" {
		t.Fatalf("got %v", err)
	}
}

func TestFlowWorkflowsListMarksReservedNamesAndBrokenWorkflows(t *testing.T) {
	home, proj := filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "proj")
	writeTestWorkflow(t, home, "issue-to-merge", `{"start":"end","params":[{"name":"issue","required":true}],"states":{"end":{"end":true}}}`)
	writeTestWorkflow(t, home, "btw", `{"start":"end","states":{"end":{"end":true}}}`)
	writeTestWorkflow(t, home, "broken", `{not json`)

	w := flowWorkflows{M: testFlowManager(flow.RepoInfo{Home: home, Root: proj}, nil)}
	byName := map[string]display.WorkflowEntry{}
	for _, e := range w.List() {
		byName[e.Name] = e
	}
	if e := byName["issue-to-merge"]; e.Err != "" || e.Source != "home" || len(e.Params) != 1 || !e.Params[0].Required {
		t.Errorf("issue-to-merge: %+v", e)
	}
	if e := byName["btw"]; !strings.Contains(e.Err, "reserved") {
		t.Errorf("btw: %+v", e)
	}
	if e := byName["broken"]; e.Err == "" {
		t.Errorf("broken: %+v", e)
	}

	errs := reservedWorkflowErrors(w.List())
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "workflow btw") {
		t.Errorf("start-up errors %v", errs)
	}
}

func TestFlowWorkflowsListIsEmptyWithoutRepository(t *testing.T) {
	m := &flow.Manager{Info: func() (flow.RepoInfo, error) { return flow.RepoInfo{}, errors.New("not in a git repository") }}
	if got := (flowWorkflows{M: m}).List(); got != nil {
		t.Fatalf("got %v", got)
	}
}
