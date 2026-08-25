package tools

import (
	"context"
	"sync"
)

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
// "report_progress" tool fails loudly until then. Guarded by
// jobProgressReporterMu for the same reason jobNotifier is (see bgbash.go's
// jobNotifierMu doc comment): it is read from job goroutines that outlive
// the tool call that started them, while SetJobProgressReporter is called
// from the setup path.
var (
	jobProgressReporterMu sync.RWMutex
	jobProgressReporter   JobProgressReporter
)

// SetJobProgressReporter wires the "report_progress" tool to a
// JobProgressReporter.
func SetJobProgressReporter(p JobProgressReporter) {
	jobProgressReporterMu.Lock()
	jobProgressReporter = p
	jobProgressReporterMu.Unlock()
}

// getJobProgressReporter copies the current JobProgressReporter out under
// RLock — see getJobAsker's doc comment (ask.go) for why callers never hold
// the lock while calling into the interface.
func getJobProgressReporter() JobProgressReporter {
	jobProgressReporterMu.RLock()
	defer jobProgressReporterMu.RUnlock()
	return jobProgressReporter
}

// ReportProgressTool implements the "report_progress" tool: lets a running
// job (any subagent call — blocking or async — or a /btw side-conversation)
// post a short status note, without ending the job. Given a job id and a
// non-empty text it always succeeds; whether anyone reads the note before
// the job finishes depends on the id actually reaching whoever is watching —
// via "wait"'s still-running response or the end-of-turn pending-jobs
// reminder (PendingLines does append it, jobs/registry.go:292), but NOT the
// jobs panel: display's job-line renderer never surfaces Progress at all. A
// blocking call under `tyci run`/`--print` hands out no job id, so there
// nobody reads it either way.
type ReportProgressTool struct{}

func (t *ReportProgressTool) Name() string { return "report_progress" }

func (t *ReportProgressTool) Run(ctx context.Context, input map[string]any) ToolResult {
	text, _ := input["text"].(string)
	if text == "" {
		return validationResult("text is required")
	}

	jobID, ok := ctx.Value(JobIDCtxKey{}).(string)
	if !ok || jobID == "" {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   "report_progress only works inside a job (a subagent call, or a /btw side-conversation) — this call has no job id",
		}
	}

	reporter := getJobProgressReporter()
	if reporter == nil {
		return ToolResult{Type: "result", Success: false, Error: "report_progress unavailable: job registry not configured"}
	}

	// Defensive: the caller's own jobID came from ctx, so this shouldn't
	// normally fail, but don't assume it silently.
	if !reporter.SetProgress(jobID, text) {
		return ToolResult{Type: "result", Success: false, Error: "failed to record progress: job_id not recognized by the registry"}
	}
	// Deliberately mode-neutral: whether anyone actually reads this before
	// the job finishes depends on the caller (see the doc comment above),
	// and a fixed claim here would be wrong for at least one of them.
	return ToolResult{Type: "result", Success: true, Content: "progress recorded"}
}
