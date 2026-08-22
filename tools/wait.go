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

// DefaultJobWaitSeconds is how long wait(job_id=...) waits when no duration is
// given, and JobMinWaitSeconds is the shortest wait that makes sense for a job.
//
// A wait on a job is not a sleep: the caller wants the RESULT. Treating it as a
// sleep is what made this tool waste turns — a model asked for one second, got
// "still running after 1s", and had learned nothing it did not already know,
// because a notice would have arrived for free. A short wait on a job is
// therefore raised to something that can actually deliver an answer, and the
// clamp note says so.
//
// 600s matches the longest a child agent can live, so waiting beyond it can
// only ever return "still running".
const DefaultJobWaitSeconds = 600
const JobMinWaitSeconds = 30

// jobPollInterval is how finely a job wait is sliced. The slices exist so the
// wait can end early for the three things that matter: the job finished, the
// job blocked on a question (and only the caller can unblock it), or a person
// typed.
const jobPollInterval = 250 * time.Millisecond

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
	// Waiting is true when the job is currently blocked inside an
	// "ask_parent" tool call, waiting for someone to call "answer_job" on
	// it. Only meaningful when Done is false.
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
	jobID, _ := input["job_id"].(string)

	secRaw, hasSeconds := input["seconds"]
	if !hasSeconds && jobID == "" {
		return ToolResult{Type: "result", Success: false, Error: "seconds is required for a plain wait (or pass job_id to wait for a job)"}
	}
	seconds := DefaultJobWaitSeconds
	if hasSeconds {
		var err error
		seconds, err = toInt(secRaw)
		if err != nil {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("seconds: %v", err)}
		}
	}

	minSeconds := MinWaitSeconds
	if jobID != "" {
		minSeconds = JobMinWaitSeconds
	}

	clampedNote := ""
	if seconds < minSeconds {
		if jobID != "" {
			// Worth explaining rather than just stating: the caller asked for
			// a sleep and is getting a wait for the result, which is what it
			// meant.
			clampedNote = fmt.Sprintf(" (asked for %ds; raised to %ds, because a shorter wait on a job can only report that it is still running — and a notice would have told you that for free)", seconds, minSeconds)
		} else {
			clampedNote = fmt.Sprintf(" (requested %ds clamped to minimum %ds)", seconds, minSeconds)
		}
		seconds = minSeconds
	} else if seconds > MaxWaitSeconds {
		clampedNote = fmt.Sprintf(" (requested %ds clamped to maximum %ds)", seconds, MaxWaitSeconds)
		seconds = MaxWaitSeconds
	}

	note, _ := input["note"].(string)

	if jobID != "" {
		if t.Waiter == nil {
			return ToolResult{Type: "result", Success: false, Error: "job registry unavailable; omit job_id to just wait N seconds"}
		}
		status, ok, interrupted := t.waitForJob(ctx, jobID, time.Duration(seconds)*time.Second)
		if !ok {
			return ToolResult{Type: "result", Success: false, Error: "unknown job_id — ids come from a backgrounded bash command, subagent(async=true), or resume; use the exact string that result gave you"}
		}
		if interrupted {
			return ToolResult{
				Type:    "result",
				Success: true,
				Content: fmt.Sprintf("stopped waiting on job %s because someone typed — read what they said and answer them. The job was not touched and is still running; you will be notified when it finishes.", jobID),
			}
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
				Content: fmt.Sprintf("job %s is waiting for an answer: %q. Relay it to the user (or genuinely-known info) unless you truly know the answer — call the \"answer_job\" tool with job_id=%q and that reply to unblock it. Never invent an answer standing in for a human who hasn't replied.%s", jobID, status.Question, jobID, clampedNote),
			}
		}
		progressNote := ""
		if status.Progress != "" {
			progressNote = fmt.Sprintf(" Latest progress: %s.", status.Progress)
		}
		return ToolResult{
			Type:    "result",
			Success: true,
			Content: fmt.Sprintf("still running after %ds (job_id=%s).%s You will be notified when it finishes — get on with other work instead of polling; wait again only if you have nothing else to do.%s", seconds, jobID, progressNote, clampedNote),
		}
	}

	sleep := t.Sleep
	if sleep == nil {
		sleep = defaultSleep
	}
	start := time.Now()
	// Sliced for the same reason a job wait is: a person typing must not have
	// to sit out someone else's sleep. A plain wait of ten minutes would
	// otherwise be ten minutes in which nothing they type is read.
	completed, interrupted := sleepInterruptibly(ctx, sleep, time.Duration(seconds)*time.Second)
	if interrupted {
		return ToolResult{
			Type:    "result",
			Success: true,
			Content: fmt.Sprintf("stopped waiting after ~%ds because someone typed — read what they said and answer them.%s", int(time.Since(start).Seconds()), clampedNote),
		}
	}
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

// waitForJob waits for a job, in slices, so it can end early for the three
// things that matter more than the remaining time:
//
//   - the job finished, which is what the caller asked for;
//   - the job blocked on a question, which only the caller can answer — and it
//     cannot answer while sitting in here, so waiting on would deadlock both
//     until the timeout;
//   - a person typed, which outranks everything.
//
// interrupted reports the last of those. ok is false for an unknown id.
func (t *WaitTool) waitForJob(ctx context.Context, jobID string, total time.Duration) (status JobStatus, ok, interrupted bool) {
	deadline := time.Now().Add(total)
	for {
		slice := jobPollInterval
		if remaining := time.Until(deadline); remaining < slice {
			slice = remaining
		}
		if slice <= 0 {
			return status, true, false
		}

		status, ok = t.Waiter.Wait(ctx, jobID, slice)
		if !ok {
			return status, false, false
		}
		if status.Done || status.Waiting {
			return status, true, false
		}
		if ctx.Err() != nil {
			return status, true, false
		}
		if UserPending() {
			return status, true, true
		}
	}
}

// sleepInterruptibly runs a plain wait in slices so it can end the moment
// someone types. completed reports that the full duration elapsed; interrupted
// reports that a person is waiting for attention.
func sleepInterruptibly(ctx context.Context, sleep func(context.Context, time.Duration) bool, total time.Duration) (completed, interrupted bool) {
	// Progress is counted in the slices asked for, not off the wall clock: the
	// sleep function is injectable, and a test one that returns without
	// actually sleeping would leave a wall-clock deadline unreachable — an
	// infinite loop rather than a fast test.
	var slept time.Duration
	for slept < total {
		slice := jobPollInterval
		if remaining := total - slept; remaining < slice {
			slice = remaining
		}
		if !sleep(ctx, slice) {
			return false, false
		}
		slept += slice
		if UserPending() {
			return false, true
		}
	}
	return true, false
}
