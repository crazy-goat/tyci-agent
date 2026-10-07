package tools

import (
	"context"
	"encoding/json"
	"sync"
)

// WorkflowManager is what the chat workflow tools call. internal/flow
// implements it; tools never imports flow (layering, as with JobWaiter).
type WorkflowManager interface {
	// Start validates and starts a run and returns at once.
	Start(ctx context.Context, workflow string, issue int) (run string, warnings []string, err error)
	// Status returns a JSON-ready summary; an empty run means the newest.
	Status(run string) (any, error)
	// Resume answers a paused run; a bad answer returns an error listing the keys.
	Resume(run, answer string) error
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

// WorkflowStartTool starts a workflow run.
type WorkflowStartTool struct{}

func (t *WorkflowStartTool) Name() string { return "workflow_start" }

func (t *WorkflowStartTool) Run(_ context.Context, input map[string]any) ToolResult {
	m, bad := getWorkflowManager()
	if bad != nil {
		return *bad
	}
	issue := intParam(input, "issue", 0)
	if issue <= 0 {
		return ToolResult{Type: "result", Success: false, Error: "workflow_start requires a positive issue number", validationError: true}
	}
	// The run outlives this tool call, so it must not use the turn context:
	// the manager runs it on its own context (cancelled on quit).
	run, warnings, err := m.Start(context.Background(), stringParam(input, "workflow", ""), issue)
	if err != nil {
		return workflowFail(err)
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
	return workflowJSON(map[string]any{"run": run, "status": "running"})
}

// workflowToolsSchema is the schema of the workflow_* tools. They are for the
// chat model only: all three are in subagentDeniedTools.
func workflowToolsSchema() []map[string]any {
	fn := func(name, desc string, props map[string]any, required []string) map[string]any {
		return map[string]any{"type": "function", "function": map[string]any{
			"name": name, "description": desc,
			"parameters": map[string]any{"type": "object", "properties": props, "required": required},
		}}
	}
	return []map[string]any{
		fn("workflow_start", "Start a workflow run for a GitHub issue of the current repository (for example \"work on #160\"). Returns at once; a notice arrives when the run finishes or pauses. Only one run is active at a time.",
			map[string]any{
				"workflow": map[string]any{"type": "string", "description": "Workflow name (default: issue-to-merge)."},
				"issue":    map[string]any{"type": "integer", "description": "Issue number."},
			}, []string{"issue"}),
		fn("workflow_status", "Show the state of a workflow run: status, current state, visits, last history entries, PR.",
			map[string]any{"run": map[string]any{"type": "string", "description": "Run id (default: newest run)."}}, []string{}),
		fn("workflow_resume", "Answer a paused workflow run. The answer must be one of the keys named in the pause notice.",
			map[string]any{
				"run":    map[string]any{"type": "string", "description": "Run id."},
				"answer": map[string]any{"type": "string", "description": "One of the allowed answers."},
			}, []string{"run", "answer"}),
	}
}
