package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// JobsTool lists the background jobs the caller may see: all of them for the
// main agent, only its own subtree for a subagent (see callerVisibleJobs).
// It is read-only; kill_job stops a job.
type JobsTool struct{}

func (t *JobsTool) Name() string { return "jobs" }

// Run returns a JSON array of job rows. By default it shows live jobs only
// (running or waiting_answer). status, when set, replaces that default, and
// all=true shows every status.
func (t *JobsTool) Run(ctx context.Context, input map[string]any) ToolResult {
	kind := stringParam(input, "kind", "")
	status := stringParam(input, "status", "")
	all := boolParam(input, "all", false)

	lister := getJobLister()
	if lister == nil {
		return ToolResult{Type: "result", Success: false, Error: "jobs: no job registry is wired"}
	}

	rows := []map[string]any{}
	for _, v := range callerVisibleJobs(ctx, lister) {
		d := v.detail
		if kind != "" && d.Kind() != kind {
			continue
		}
		switch {
		case status != "":
			if d.Status() != status {
				continue
			}
		case !all:
			if !isLiveStatus(d.Status()) {
				continue
			}
		}
		rows = append(rows, map[string]any{
			"id":          v.src.ID(),
			"short_id":    shortID(v.src.ID()),
			"kind":        d.Kind(),
			"status":      d.Status(),
			"description": d.Description(),
			"parent":      v.src.ParentID(),
			"age":         time.Since(d.StartedAt()).Round(time.Second).String(),
			"question":    d.Question(),
		})
	}

	b, err := json.Marshal(rows)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("jobs: %v", err)}
	}
	return ToolResult{Type: "result", Success: true, Content: string(b)}
}
