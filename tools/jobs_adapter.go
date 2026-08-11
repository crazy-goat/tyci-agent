package tools

import (
	"context"
	"time"

	"github.com/decodo/tyci/jobs"
)

// jobRegistry is the single, process-wide background job registry shared by
// the "subagent" tool's async mode (producer) and the "wait" tool (consumer),
// analogous to lockRegistry in lock.go.
var jobRegistry = jobs.NewRegistry()

// jobRegistryWaiter adapts *jobs.Registry to the tools.JobWaiter contract
// that wait.go defines independently of this package's jobs dependency, so
// wait.go itself never has to import jobs.
type jobRegistryWaiter struct{ r *jobs.Registry }

func (w jobRegistryWaiter) Wait(ctx context.Context, id string, timeout time.Duration) (JobStatus, bool) {
	job, ok := w.r.Wait(ctx, id, timeout)
	if !ok {
		return JobStatus{}, false
	}
	status := JobStatus{ID: job.ID, Done: job.Status != jobs.StatusRunning}
	switch job.Status {
	case jobs.StatusDone:
		status.Success = true
		status.Content = job.Result
	case jobs.StatusTruncated:
		status.Success = true
		status.Content = job.Result + " [truncated: hit its iteration cap]"
	case jobs.StatusFailed:
		status.Success = false
		status.Error = job.Err
	}
	return status, true
}
