package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
// Keep the default finite so a parent regains a turn to inspect progress or
// cancel a job; this is an observation timeout, not a subagent execution cap.
const DefaultJobWaitSeconds = MaxWaitSeconds
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
	// Progress is the last status note recorded for the job, if any —
	// either an explicit "report_progress" call, or (the dominant source in
	// practice) one throttled line of a backgrounded shell command's own
	// output, see tools/bash.go's bashRun.setProgress. Persists after the
	// job finishes too.
	Progress string
	// ProgressHistory is every such note recorded for the job, oldest
	// first — the sequence Progress alone loses once a second note
	// overwrites the first. May be shorter than the job's true call count:
	// the registry bounds how many it retains (see
	// ProgressHistoryTruncated).
	ProgressHistory []string
	// ProgressHistoryTruncated is true once the registry has evicted at
	// least one older entry from ProgressHistory to keep it bounded — see
	// jobs.Job.ProgressHistoryTruncated, which this mirrors. For a
	// backgrounded shell command producing steady output this becomes true
	// quickly (its notes arrive roughly once a second, so ~20s in); it is
	// NOT a sign anything went wrong.
	ProgressHistoryTruncated bool
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
	//
	// Assigning this field directly (as tests do — `&WaitTool{Waiter: w}`)
	// is fine for a private instance nothing else can reach yet: the write
	// happens before the instance is shared with any other goroutine. The
	// registered "wait" singleton in toolRegistry is different: SetJobWaiter
	// (tool.go) mutates it in place, after Run and waitForJob below may
	// already be reading it on other job/agent goroutines — a data race on
	// the field itself, which guarding toolRegistry's map access does
	// nothing for (the race isn't on the map slot). So Run/waitForJob never
	// read Waiter directly; they go through waiter(), and SetJobWaiter goes
	// through setWaiter, both guarded by waiterMu. Test code that builds its
	// own WaitTool and only ever touches Waiter from one goroutine can keep
	// assigning the field directly — waiterMu's zero value works with no
	// setup.
	Waiter JobWaiter

	waiterMu sync.RWMutex

	// Sleep is overridable for tests. nil uses the default context-aware
	// sleep (defaultSleep). Returns false if ctx was cancelled before d
	// elapsed, true if the full duration elapsed.
	Sleep func(ctx context.Context, d time.Duration) bool
}

func (t *WaitTool) Name() string { return "wait" }

// waiter copies the current Waiter out under RLock — same reason as every
// other getter in this package (see bgbash.go's jobNotifierMu doc comment):
// the caller must not hold waiterMu while calling into the JobWaiter it
// returns.
func (t *WaitTool) waiter() JobWaiter {
	t.waiterMu.RLock()
	defer t.waiterMu.RUnlock()
	return t.Waiter
}

// setWaiter installs w as this instance's Waiter under Lock. SetJobWaiter
// (tool.go) is the only production caller, wiring the registered "wait"
// singleton once from main()'s setup path — but that write still races with
// Run/waitForJob reading the field on job/agent goroutines started earlier,
// hence the lock.
func (t *WaitTool) setWaiter(w JobWaiter) {
	t.waiterMu.Lock()
	t.Waiter = w
	t.waiterMu.Unlock()
}

func (t *WaitTool) Run(ctx context.Context, input map[string]any) ToolResult {
	jobID, _ := input["job_id"].(string)

	secRaw, hasSeconds := input["seconds"]
	if !hasSeconds && jobID == "" {
		return validationResult("seconds is required for a plain wait (or pass job_id to wait for a job)")
	}
	seconds := DefaultJobWaitSeconds
	if hasSeconds {
		var err error
		seconds, err = toInt(secRaw)
		if err != nil {
			return validationResultf("seconds: %v", err)
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
		// Copy the interface value out under RLock once, then use this
		// local copy for both the nil check and the actual wait below —
		// same reason as getJobStarter's doc comment (subagent.go): a
		// second read via t.waiter() after the nil check could observe a
		// different value if SetJobWaiter ran in between.
		waiter := t.waiter()
		if waiter == nil {
			return ToolResult{Type: "result", Success: false, Error: "job registry unavailable; omit job_id to just wait N seconds"}
		}
		status, ok, interrupted := t.waitForJob(ctx, waiter, jobID, time.Duration(seconds)*time.Second)
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
		// Show the whole retained sequence, not just the latest note: a
		// job that reported several times (a backgrounded shell command's
		// own output is the dominant source of these, not just an explicit
		// report_progress call — see JobStatus.Progress's doc comment) has
		// told the parent several different things, and collapsing that
		// down to "the last one" here would silently discard the same
		// information SetProgress's history exists to keep (item 53).
		// Fall back to Progress alone only for a JobWaiter implementation
		// that never populates ProgressHistory.
		//
		// progressNote and progressSep together decide how the sentence
		// after it starts: a single note (or none) stays on the SAME line,
		// exactly as this read before ProgressHistory existed, while an
		// actual multi-entry block gets its own paragraph so "You will be
		// notified..." does not run on straight from the last bullet.
		progressNote := ""
		progressSep := " "
		if len(status.ProgressHistory) > 1 {
			block, dropped := renderProgressHistory(status.ProgressHistory, progressHistoryPreviewRuneBudget)
			// Both budget-side dropping (computed just above) and
			// registry-side eviction (status.ProgressHistoryTruncated,
			// which carries no count of its own — see its doc comment)
			// mean the same thing to whoever reads this: notes existed
			// that are not shown. Folded into one line rather than two,
			// per review E2.
			omittedNote := ""
			switch {
			case dropped > 0 && status.ProgressHistoryTruncated:
				omittedNote = fmt.Sprintf(" (%d older note(s) omitted here for length, plus earlier ones already dropped by the registry's cap)", dropped)
			case dropped > 0:
				omittedNote = fmt.Sprintf(" (%d older note(s) omitted here for length)", dropped)
			case status.ProgressHistoryTruncated:
				omittedNote = " (earlier notes already dropped by the registry's cap)"
			}
			progressNote = fmt.Sprintf(" Progress so far%s:\n%s", omittedNote, block)
			progressSep = "\n"
		} else if len(status.ProgressHistory) == 1 {
			// flattenProgressLine here too, not just in the multi-entry
			// block: a single note can contain "\n" exactly as easily as
			// one of several can (tools/progress.go's Run rejects only the
			// empty string), and a raw line break dropped mid-sentence into
			// this result is the same defect the block rendering exists to
			// avoid. No truncation note is possible in this shape — the
			// registry only sets ProgressHistoryTruncated once it has
			// evicted, which pins the length at progressHistoryCap (20), so
			// a one-entry history cannot also be truncated. That is the only
			// reason it is safe to render inline without one.
			progressNote = fmt.Sprintf(" Latest progress: %s.", flattenProgressLine(status.ProgressHistory[0]))
		} else if status.Progress != "" {
			// Same flattening for the same reason. This branch only runs for
			// a JobWaiter that never populates ProgressHistory at all.
			progressNote = fmt.Sprintf(" Latest progress: %s.", flattenProgressLine(status.Progress))
		}
		return ToolResult{
			Type:    "result",
			Success: true,
			Content: fmt.Sprintf("still running after %ds (job_id=%s).%s%sYou will be notified when it finishes — get on with other work instead of polling; wait again only if you have nothing else to do.%s", seconds, jobID, progressNote, progressSep, clampedNote),
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

// progressHistoryPreviewRuneBudget bounds the AGGREGATE size of the
// rendered progress-history block wait() hands back, across however many
// entries there are — not a per-entry cap (SetProgress already applies
// one of those, see jobs/registry.go's progressEntryRuneCap). wait is a
// tool the model calls repeatedly while a job is still running, and every
// poll re-pays whatever this renders into that model's own permanent
// conversation history, so this exists to bound what gets typed into the
// context window over and over, not to bound registry memory. Same idiom
// as subagentCompletionNotice's preview (tools/subagent.go's
// subagentNoticePreviewLimit = 800): one rune budget for the whole block.
const progressHistoryPreviewRuneBudget = 800

// flattenProgressLine collapses a single progress entry's internal
// newlines to spaces, same idiom as session.cleanDumpText and
// display.Minimal.singleLine use elsewhere in this codebase for the same
// reason: report_progress's text is model-supplied and nothing upstream
// rejects a note containing "\n" (see tools/progress.go's Run), so without
// this a multi-line note would be indistinguishable, once several entries
// are joined, from several separate notes.
func flattenProgressLine(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " "))
}

// renderProgressHistory renders history (oldest first) as one
// "- "-prefixed, newline-separated block — each entry flattened via
// flattenProgressLine first — fit within budget runes in total. When the
// budget cannot hold every entry, the OLDEST are dropped first: whoever
// calls wait() on a running job wants to know what it is doing NOW, not
// how it started, and dropped reports how many entries the BUDGET left
// out, so the caller can fold that into one honest line together with any
// registry-side eviction (which carries no count of its own). Entries that
// are empty once flattened are not counted in dropped — they are not notes
// anybody wanted to read, so reporting them as omitted would overstate
// what was lost.
//
// At least one entry (the newest) is always kept, even if it alone
// exceeds budget, so a single long note still reads as something rather
// than nothing.
func renderProgressHistory(history []string, budget int) (rendered string, dropped int) {
	if len(history) == 0 {
		return "", 0
	}
	// Drop entries that are empty once flattened. This is not hypothetical
	// tidying: tools/bash.go's pump posts EVERY output line of a
	// backgrounded command, blank separator lines included, and
	// report_progress's own empty-string rejection does not apply to that
	// path — so without this a build that prints blank lines renders as a
	// run of empty bullets.
	flattened := make([]string, 0, len(history))
	for _, entry := range history {
		if line := flattenProgressLine(entry); line != "" {
			flattened = append(flattened, line)
		}
	}
	if len(flattened) == 0 {
		return "", 0
	}

	// Walk newest-to-oldest, accumulating rune cost (entry plus its
	// "- " marker and trailing newline), so the survivors are always the
	// newest entries. start is the index of the oldest entry kept.
	used := 0
	start := len(flattened)
	for start > 0 {
		entry := flattened[start-1]
		cost := len([]rune(entry)) + len([]rune("- \n"))
		if used+cost > budget && start != len(flattened) {
			break
		}
		used += cost
		start--
	}

	var b strings.Builder
	for _, entry := range flattened[start:] {
		b.WriteString("- ")
		b.WriteString(entry)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), start
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
func (t *WaitTool) waitForJob(ctx context.Context, waiter JobWaiter, jobID string, total time.Duration) (status JobStatus, ok, interrupted bool) {
	deadline := time.Now().Add(total)
	for {
		slice := jobPollInterval
		if remaining := time.Until(deadline); remaining < slice {
			slice = remaining
		}
		if slice <= 0 {
			return status, true, false
		}

		status, ok = waiter.Wait(ctx, jobID, slice)
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
