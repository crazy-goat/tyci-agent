package tools

import "context"

// JobProgressReporter is the local contract the "report_progress" tool
// needs — set once from main() via SetJobProgressReporter, over the app's
// shared jobs.Registry. Same layering rule as the other job-facing tools in
// this package.
type JobProgressReporter interface {
	// SetProgress records a short status note for the job identified by id.
	// Returns false if id is unknown.
	SetProgress(id, text string) bool
}

// jobProgressReporter is nil until SetJobProgressReporter is called; the
// "report_progress" tool fails loudly until then.
var jobProgressReporter JobProgressReporter

// SetJobProgressReporter wires the "report_progress" tool to a
// JobProgressReporter.
func SetJobProgressReporter(p JobProgressReporter) { jobProgressReporter = p }

// ReportProgressTool implements the "report_progress" tool: lets a running
// background job post a short status note visible via "wait"'s still-running
// response and the jobs panel, without ending the job.
type ReportProgressTool struct{}

func (t *ReportProgressTool) Name() string { return "report_progress" }

func (t *ReportProgressTool) Run(ctx context.Context, input map[string]any) ToolResult {
	text, _ := input["text"].(string)
	if text == "" {
		return ToolResult{Type: "result", Success: false, Error: "text is required"}
	}

	jobID, ok := ctx.Value(JobIDCtxKey{}).(string)
	if !ok || jobID == "" {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   "report_progress only works inside an async background job (started via subagent(...,async:true) or a /btw side-conversation)",
		}
	}

	if jobProgressReporter == nil {
		return ToolResult{Type: "result", Success: false, Error: "report_progress unavailable: job registry not configured"}
	}

	// Defensive: the caller's own jobID came from ctx, so this shouldn't
	// normally fail, but don't assume it silently.
	if !jobProgressReporter.SetProgress(jobID, text) {
		return ToolResult{Type: "result", Success: false, Error: "failed to record progress: job_id not recognized by the registry"}
	}
	return ToolResult{Type: "result", Success: true, Content: "progress recorded; whoever checks this job with wait can see it now"}
}
