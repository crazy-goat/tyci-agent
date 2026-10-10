package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// WorkflowManager is what the chat workflow tools call. internal/flow
// implements it; tools never imports flow (layering, as with JobWaiter).
type WorkflowManager interface {
	// Start validates and starts a run and returns at once. params are the
	// positional param values of the workflow.
	Start(ctx context.Context, workflow string, params []string) (run string, warnings []string, err error)
	// Workflows lists the workflows that can be started.
	Workflows() []WorkflowInfo
	// Status returns a JSON-ready summary; an empty run means the newest.
	Status(run string) (any, error)
	// List returns one page of the runs of the repo, newest first, as a JSON-ready value.
	// A limit of 0 means the default page size.
	List(archived bool, limit, offset int) (any, error)
	// Resume answers a paused run; a bad answer returns an error listing the keys.
	Resume(run, answer string) error
	// Stop ends an active run and returns a JSON-ready summary of it.
	Stop(run, reason string) (any, error)
}

// WorkflowInfo describes a workflow for the model.
type WorkflowInfo struct {
	Name        string
	Description string
	Params      []WorkflowParam
}

// WorkflowParam is one positional param of a workflow.
type WorkflowParam struct {
	Name        string
	Description string
	Required    bool
}

var (
	workflowMgrMu sync.RWMutex
	workflowMgr   WorkflowManager
)

// SetWorkflowManager wires the workflow_* tools. Until it is called they fail
// with a clear error.
func SetWorkflowManager(m WorkflowManager) {
	workflowMgrMu.Lock()
	workflowMgr = m
	workflowMgrMu.Unlock()
}

func workflowManagerSet() bool {
	workflowMgrMu.RLock()
	defer workflowMgrMu.RUnlock()
	return workflowMgr != nil
}

func getWorkflowManager() (WorkflowManager, *ToolResult) {
	workflowMgrMu.RLock()
	defer workflowMgrMu.RUnlock()
	if workflowMgr == nil {
		return nil, &ToolResult{Type: "result", Success: false, Error: "workflow tools are unavailable in this mode"}
	}
	return workflowMgr, nil
}

func workflowFail(err error) ToolResult {
	return ToolResult{Type: "result", Success: false, Error: err.Error()}
}

func workflowJSON(v any) ToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return workflowFail(err)
	}
	return ToolResult{Type: "result", Success: true, Content: string(b)}
}

// StopWorkflowRun stops an active run through the wired workflow manager. It is
// used by the workflow_stop tool, by the /stop command and by the Runs tab key.
func StopWorkflowRun(run, reason string) (any, error) {
	m, bad := getWorkflowManager()
	if bad != nil {
		return nil, errors.New(bad.Error)
	}
	return m.Stop(strings.TrimSpace(run), reason)
}

// WorkflowStartTool starts a workflow run.
type WorkflowStartTool struct{}

func (t *WorkflowStartTool) Name() string { return "workflow_start" }

func (t *WorkflowStartTool) Run(_ context.Context, input map[string]any) ToolResult {
	m, bad := getWorkflowManager()
	if bad != nil {
		return *bad
	}
	list := m.Workflows()
	name := stringParam(input, "workflow", "")
	if name == "" {
		return ToolResult{Type: "result", Success: false, validationError: true,
			Error: "workflow is required. Available workflows: " + workflowSummary(list)}
	}
	if _, hasIssue := input["issue"]; hasIssue {
		return ToolResult{Type: "result", Success: false, validationError: true,
			Error: `the "issue" argument is gone: use params instead, for example "params": ["160"]`}
	}
	params, err := workflowParams(input)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error(), validationError: true}
	}
	// The run outlives this tool call, so it must not use the turn context:
	// the manager runs it on its own context (cancelled on quit).
	run, warnings, err := m.Start(context.Background(), name, params)
	if err != nil {
		msg := err.Error()
		if len(list) > 0 && !workflowKnown(list, name) && !strings.Contains(msg, "available") {
			msg += ". Available workflows: " + workflowSummary(list)
		}
		return ToolResult{Type: "result", Success: false, Error: msg}
	}
	if warnings == nil {
		warnings = []string{}
	}
	return workflowJSON(map[string]any{"run": run, "status": "running", "warnings": warnings})
}

// WorkflowStatusTool reports the state of a run.
type WorkflowStatusTool struct{}

func (t *WorkflowStatusTool) Name() string { return "workflow_status" }

func (t *WorkflowStatusTool) Run(_ context.Context, input map[string]any) ToolResult {
	m, bad := getWorkflowManager()
	if bad != nil {
		return *bad
	}
	st, err := m.Status(stringParam(input, "run", ""))
	if err != nil {
		return workflowFail(err)
	}
	return workflowJSON(st)
}

// WorkflowStopTool stops an active run. The worktree and the pull request stay.
type WorkflowStopTool struct{}

func (t *WorkflowStopTool) Name() string { return "workflow_stop" }

func (t *WorkflowStopTool) Run(_ context.Context, input map[string]any) ToolResult {
	run := stringParam(input, "run", "")
	if run == "" {
		return ToolResult{Type: "result", Success: false, Error: "workflow_stop requires run", validationError: true}
	}
	st, err := StopWorkflowRun(run, stringParam(input, "reason", ""))
	if err != nil {
		return workflowFail(err)
	}
	return workflowJSON(st)
}

// WorkflowListTool lists the runs of the repo, newest first.
type WorkflowListTool struct{}

func (t *WorkflowListTool) Name() string { return "workflow_list" }

func (t *WorkflowListTool) Run(_ context.Context, input map[string]any) ToolResult {
	m, bad := getWorkflowManager()
	if bad != nil {
		return *bad
	}
	list, err := m.List(boolParam(input, "archived", false), intParam(input, "limit", 0), intParam(input, "offset", 0))
	if err != nil {
		return workflowFail(err)
	}
	return workflowJSON(list)
}

// WorkflowResumeTool answers a paused run.
type WorkflowResumeTool struct{}

func (t *WorkflowResumeTool) Name() string { return "workflow_resume" }

func (t *WorkflowResumeTool) Run(_ context.Context, input map[string]any) ToolResult {
	m, bad := getWorkflowManager()
	if bad != nil {
		return *bad
	}
	run, answer := stringParam(input, "run", ""), stringParam(input, "answer", "")
	if run == "" || answer == "" {
		return ToolResult{Type: "result", Success: false, Error: "workflow_resume requires run and answer", validationError: true}
	}
	if err := m.Resume(run, answer); err != nil {
		return workflowFail(err)
	}
	if answer == "apply" || answer == "reject" {
		// A proposal answer keeps the run paused; the status says what happened.
		if st, err := m.Status(run); err == nil {
			return workflowJSON(st)
		}
	}
	return workflowJSON(map[string]any{"run": run, "status": "running"})
}

// workflowList returns the workflows of the manager, or none when it is not set.
func workflowList() []WorkflowInfo {
	m, bad := getWorkflowManager()
	if bad != nil {
		return nil
	}
	return m.Workflows()
}

// workflowLine describes one workflow: "name p1 [p2]: description". A required
// param has no brackets, an optional one has them.
func workflowLine(w WorkflowInfo) string {
	var b strings.Builder
	b.WriteString(w.Name)
	for _, p := range w.Params {
		if p.Required {
			b.WriteString(" " + p.Name)
		} else {
			b.WriteString(" [" + p.Name + "]")
		}
	}
	b.WriteString(":")
	if w.Description != "" {
		b.WriteString(" " + w.Description)
	}
	return b.String()
}

// workflowSummary joins the lines of workflows for an error text.
func workflowSummary(list []WorkflowInfo) string {
	if len(list) == 0 {
		return "none"
	}
	lines := make([]string, 0, len(list))
	for _, w := range list {
		lines = append(lines, workflowLine(w))
	}
	return strings.Join(lines, "; ")
}

// workflowKnown reports whether name is one of list.
func workflowKnown(list []WorkflowInfo, name string) bool {
	for _, w := range list {
		if w.Name == name {
			return true
		}
	}
	return false
}

// workflowParams reads the "params" argument. JSON gives numbers as float64 and
// arrays as []any; both are converted to the text of each value.
func workflowParams(input map[string]any) ([]string, error) {
	const bad = `params must be an array of strings, for example ["160"]`
	switch v := input["params"].(type) {
	case nil:
		return nil, nil
	case []string:
		return v, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			switch x := item.(type) {
			case nil:
				out = append(out, "")
			case string:
				out = append(out, x)
			case float64:
				out = append(out, strconv.FormatFloat(x, 'f', -1, 64))
			default:
				out = append(out, fmt.Sprint(x))
			}
		}
		return out, nil
	default:
		return nil, errors.New(bad)
	}
}

// workflowToolsSchema is the schema of the workflow_* tools. They are for the
// chat model only: all of them are in subagentDeniedTools. The start description
// lists list, so the model picks a real workflow.
func workflowToolsSchema(list []WorkflowInfo) []map[string]any {
	fn := func(name, desc string, props map[string]any, required []string) map[string]any {
		return map[string]any{"type": "function", "function": map[string]any{
			"name": name, "description": desc,
			"parameters": map[string]any{"type": "object", "properties": props, "required": required},
		}}
	}
	startDesc := "Start a workflow run. Returns at once; a notice arrives when the run finishes or pauses."
	if len(list) == 0 {
		startDesc += " No workflows were found at session start. If you ask for a name, the tool checks the current list and returns it."
	} else {
		startDesc += " Workflows at session start (a workflow added later is not listed here; if the name is unknown, the error gives the current list):"
		for _, w := range list {
			startDesc += "\n- " + workflowLine(w)
		}
	}
	return []map[string]any{
		fn("workflow_start", startDesc,
			map[string]any{
				"workflow": map[string]any{"type": "string", "description": "Workflow name, one of the available workflows in the description."},
				"params": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
					"description": "Positional param values, in the order the workflow declares them."},
			}, []string{"workflow"}),
		fn("workflow_status", "Show the state of a workflow run: status, current state, visits, last history entries, PR.",
			map[string]any{"run": map[string]any{"type": "string", "description": "Run id (default: newest run)."}}, []string{}),
		fn("workflow_list", "List the workflow runs of this project, newest first: run id, workflow, params, status, state, start time. By default only running and paused runs. With archived true also done, stopped and failed runs. Use limit (default 20) and offset to page; the result gives total and more.",
			map[string]any{
				"archived": map[string]any{"type": "boolean", "description": "Also list done, stopped and failed runs. Default false."},
				"limit":    map[string]any{"type": "number", "description": "Runs per page. Default 20."},
				"offset":   map[string]any{"type": "number", "description": "Runs to skip, newest first. Default 0."},
			}, []string{}),
		fn("workflow_stop", "Stop an active workflow run. The worktree and the pull request stay. Use workflow_status first if the run is not known.",
			map[string]any{
				"run":    map[string]any{"type": "string", "description": "Run id."},
				"reason": map[string]any{"type": "string", "description": "Why the run stops. Optional."},
			}, []string{"run"}),
		fn("workflow_resume", "Answer a paused workflow run. Use one of the answers named in the pause notice: \"retry\" (back to the worker), \"stop\" (end the run), \"retry <note>\" (back to the worker with the note), \"goto <state>\" (continue at that state) or \"resume\" (only for a run paused at start-up: continue at its saved state). When the pause has a workflow proposal, first show its summary and patch from workflow_status to the user, then answer \"apply\" (opens a PR with the change of .tyci/workflows/<name>/) or \"reject\" only as the user says; the run stays paused for its normal answer.",
			map[string]any{
				"run":    map[string]any{"type": "string", "description": "Run id."},
				"answer": map[string]any{"type": "string", "description": "One of the allowed answers."},
			}, []string{"run", "answer"}),
	}
}
