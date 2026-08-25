package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// JobResumer is the local contract the "resume" tool needs — set once from
// main() via SetJobResumer, over the app's shared jobs.Registry. Same
// layering rule as the other job-facing tools in this package.
type JobResumer interface {
	// Resume continues a finished async job's conversation with a new user
	// turn (task) and returns a handle to the NEW job that runs it — the
	// original job_id is not reused. Returns an error if jobID is unknown
	// or was never resumable (e.g. it was a synchronous subagent call, or
	// it failed hard rather than finishing/truncating).
	Resume(ctx context.Context, jobID, task string) (JobHandle, error)
}

// jobResumer is nil until SetJobResumer is called; the "resume" tool fails
// loudly until then. Guarded by jobResumerMu for the same reason jobNotifier
// is (see bgbash.go's jobNotifierMu doc comment).
var (
	jobResumerMu sync.RWMutex
	jobResumer   JobResumer
)

// SetJobResumer wires the "resume" tool to a JobResumer.
func SetJobResumer(r JobResumer) {
	jobResumerMu.Lock()
	jobResumer = r
	jobResumerMu.Unlock()
}

// getJobResumer copies the current JobResumer out under RLock — see
// getJobAsker's doc comment (ask.go) for why callers never hold the lock
// while calling into the interface.
func getJobResumer() JobResumer {
	jobResumerMu.RLock()
	defer jobResumerMu.RUnlock()
	return jobResumer
}

// ResumeTool implements the "resume" tool: continues an already-finished
// async job's conversation with a new message, as a brand-new job. Poll the
// new job_id with "wait" like any other async job.
type ResumeTool struct{}

func (t *ResumeTool) Name() string { return "resume" }

func (t *ResumeTool) Run(ctx context.Context, input map[string]any) ToolResult {
	jobID, _ := input["job_id"].(string)
	if jobID == "" {
		return validationResult("job_id is required")
	}
	task, _ := input["task"].(string)
	if task == "" {
		return validationResult("task is required")
	}

	resumer := getJobResumer()
	if resumer == nil {
		return ToolResult{Type: "result", Success: false, Error: "resume unavailable: job registry not configured"}
	}

	handle, err := resumer.Resume(ctx, jobID, task)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	data, err := json.Marshal(struct {
		JobID string `json:"job_id"`
	}{JobID: handle.ID()})
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("marshal resumed job: %v", err)}
	}
	return ToolResult{
		Type:    "result",
		Success: true,
		// The bare id taught nothing. What matters about resume is the part
		// that is easy to miss: the job still has its whole conversation, so
		// a follow-up costs no re-explaining.
		Content: string(data) + "\nIt runs in the background with its previous conversation intact, so you did not have to restate any context. You will be notified when it finishes; read the result then with wait(job_id=...).",
	}
}
