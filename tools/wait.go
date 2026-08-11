package tools

import (
	"context"
	"fmt"
	"time"
)

// MaxWaitSeconds and MinWaitSeconds bound the "seconds" input. A caller that
// asks for more/less than this range is clamped, not rejected — see Run.
const MaxWaitSeconds = 1800
const MinWaitSeconds = 1

// JobStatus and JobWaiter are a LOCAL contract owned by the tools package.
// Do not import a "jobs" package here: on this branch it doesn't exist yet,
// and even once it does, tools must not depend on it (that would risk an
// import cycle if jobs ever needs tools). A future jobs.Registry satisfies
// JobWaiter structurally — no changes needed here when it's wired in.
type JobStatus struct {
	ID      string
	Done    bool
	Success bool
	Content string
	Error   string
	// Waiting is true when the job is currently blocked inside an "ask"
	// tool call, waiting for someone to call "answer" on it. Only
	// meaningful when Done is false.
	Waiting bool
	// Question is the pending question text while Waiting is true.
	Question string
	// Progress is the last status note the job reported via
	// "report_progress", if any. Persists after the job finishes too.
	Progress string
}

type JobWaiter interface {
	// Wait blocks until the job identified by id finishes or timeout elapses
	// (whichever comes first), or ctx is cancelled. ok is false when id is
	// unknown to the registry.
	Wait(ctx context.Context, id string, timeout time.Duration) (status JobStatus, ok bool)
}

// WaitTool lets the model deliberately wait for a period of time (plain
// wait) or poll a background job's status (once Waiter is wired up).
type WaitTool struct {
	// Waiter is nilable. When nil, job_id requests fail with a clear error
	// instead of panicking; omitting job_id (plain wait) works regardless.
	Waiter JobWaiter

	// Sleep is overridable for tests. nil uses the default context-aware
	// sleep (defaultSleep). Returns false if ctx was cancelled before d
	// elapsed, true if the full duration elapsed.
	Sleep func(ctx context.Context, d time.Duration) bool
}

func (t *WaitTool) Name() string { return "wait" }

func (t *WaitTool) Run(ctx context.Context, input map[string]any) ToolResult {
	secRaw, ok := input["seconds"]
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "seconds is required"}
	}
	seconds, err := toInt(secRaw)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("seconds: %v", err)}
	}

	clampedNote := ""
	if seconds < MinWaitSeconds {
		clampedNote = fmt.Sprintf(" (requested %ds clamped to minimum %ds)", seconds, MinWaitSeconds)
		seconds = MinWaitSeconds
	} else if seconds > MaxWaitSeconds {
		clampedNote = fmt.Sprintf(" (requested %ds clamped to maximum %ds)", seconds, MaxWaitSeconds)
		seconds = MaxWaitSeconds
	}

	jobID, _ := input["job_id"].(string)
	note, _ := input["note"].(string)

	if jobID != "" {
		if t.Waiter == nil {
			return ToolResult{Type: "result", Success: false, Error: "job registry unavailable; omit job_id to just wait N seconds"}
		}
		status, ok := t.Waiter.Wait(ctx, jobID, time.Duration(seconds)*time.Second)
		if !ok {
			return ToolResult{Type: "result", Success: false, Error: "unknown job_id"}
		}
		if status.Done {
			if status.Success {
				return ToolResult{Type: "result", Success: true, Content: "job finished: " + status.Content + clampedNote}
			}
			return ToolResult{Type: "result", Success: false, Error: status.Error}
		}
		if status.Waiting {
			return ToolResult{
				Type:    "result",
				Success: true,
				Content: fmt.Sprintf("job %s is waiting for an answer: %q. Call the \"answer\" tool with job_id=%q and your reply to unblock it.%s", jobID, status.Question, jobID, clampedNote),
			}
		}
		progressNote := ""
		if status.Progress != "" {
			progressNote = fmt.Sprintf(" Latest progress: %s.", status.Progress)
		}
		return ToolResult{
			Type:    "result",
			Success: true,
			Content: fmt.Sprintf("still running after %ds (job_id=%s). Call wait again to keep polling.%s%s", seconds, jobID, progressNote, clampedNote),
		}
	}

	sleep := t.Sleep
	if sleep == nil {
		sleep = defaultSleep
	}
	start := time.Now()
	completed := sleep(ctx, time.Duration(seconds)*time.Second)
	if !completed {
		elapsed := time.Since(start)
		return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("wait cancelled after ~%ds%s", int(elapsed.Seconds()), clampedNote)}
	}

	content := fmt.Sprintf("waited %ds", seconds)
	if note != "" {
		content += fmt.Sprintf(" (%s)", note)
	}
	content += "; check status now."
	content += clampedNote
	return ToolResult{Type: "result", Success: true, Content: content}
}

// defaultSleep blocks for d or until ctx is cancelled, whichever comes
// first. A single select on ctx.Done()/time.After(d) — no ticking loop —
// so cancellation is immediate regardless of d's length.
func defaultSleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
