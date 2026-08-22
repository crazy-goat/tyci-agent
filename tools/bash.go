package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/decodo/tyci/stream"
)

// exitCodeHint returns a human-readable description for common exit codes.
// Returns empty string for 0 or unknown codes.
func exitCodeHint(code int) string {
	switch code {
	case 1:
		return "general error"
	case 2:
		return "misuse of shell builtins"
	case 126:
		return "command cannot execute (permission/not executable)"
	case 127:
		return "command not found"
	case 128:
		return "invalid exit argument"
	case 130:
		return "script terminated by Ctrl+C (SIGINT)"
	case 137:
		return "process killed (SIGKILL, likely out-of-memory)"
	case 139:
		return "segmentation fault (SIGSEGV)"
	case 143:
		return "process terminated (SIGTERM)"
	case 255:
		return "exit status out of range"
	default:
		if code >= 129 && code <= 159 {
			sig := code - 128
			return fmt.Sprintf("killed by signal %d", sig)
		}
		return ""
	}
}

// formatExitError formats a non-zero exit error with code and hint.
func formatExitError(err error, output string) string {
	exitCode := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	}
	hint := exitCodeHint(exitCode)
	if hint != "" {
		return fmt.Sprintf("❌ exit code %d (%s):\n%s", exitCode, hint, output)
	}
	return fmt.Sprintf("❌ exit code %d:\n%s", exitCode, output)
}

type BashTool struct{}

func (t *BashTool) Name() string {
	return "bash"
}

// Run executes a shell command. It normally blocks until the command exits,
// exactly as it always has. What is new is the escape hatch for commands that
// outlive the agent's patience: after BashBackgroundAfterSec (or immediately,
// with run_in_background) the still-running command is handed to the job
// registry and Run returns a job_id instead of waiting. See handoff for why
// that requires the tool — not the dispatcher — to own the process lifetime.
func (t *BashTool) Run(ctx context.Context, input map[string]any) ToolResult {
	cmdVal, ok := input["command"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "command required"}
	}
	if cmdVal == "" {
		return ToolResult{Type: "result", Success: false, Error: "empty command"}
	}

	timeoutSec := intParam(input, "timeout", BashDefaultTimeoutSec)
	if timeoutSec <= 0 {
		timeoutSec = BashDefaultTimeoutSec
	}

	// How long to wait before moving the command to the background.
	//
	// An earlier version treated an explicit "timeout" as the caller saying
	// how long it was willing to block, and disabled the handoff. That was
	// wrong in practice: models fill in optional parameters as a matter of
	// habit — a real session showed "timeout": 600 on every single bash call
	// — so the handoff was switched off almost every time and the feature
	// effectively did not exist.
	//
	// A large timeout now means only what it says: the command is allowed to
	// run that long. It still moves to the background at the threshold,
	// which costs the caller nothing — the command keeps running and it can
	// block on wait(job_id) for as long as it likes. Insisting on the
	// foreground is what background_after=0 is for, which says so plainly
	// instead of being inferred.
	bgAfterSec := intParam(input, "background_after", -1)
	if bgAfterSec < 0 {
		bgAfterSec = BashBackgroundAfterSec
	}
	if bgAfterSec >= timeoutSec {
		// The command would hit its own timeout first; handing off later than
		// that could never happen, so don't arm the timer at all.
		bgAfterSec = 0
	}

	runInBg := boolParam(input, "run_in_background", false)
	note := ""
	if !backgroundAllowed(ctx) {
		bgAfterSec = 0
		if runInBg {
			runInBg = false
			note = "\n\n[note: run_in_background was requested, but background commands are unavailable here (one-shot run, or inside a subagent) — ran in the foreground instead]"
		}
	}

	label := stringParam(input, "description", "")
	if label == "" {
		label = truncateLine(strings.TrimSpace(firstLine(cmdVal)), 60)
	}

	run, err := startBash(ctx, cmdVal)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	if runInBg {
		if res, ok := t.handoff(ctx, run, label, 0); ok {
			return res
		}
		// Every background slot is busy. Falling back to the foreground is
		// the honest outcome: the command is already running, so refusing
		// now would mean killing it, and silently exceeding the cap would
		// defeat the point of having one.
		note = fmt.Sprintf("\n\n[note: run_in_background was requested, but all %d background slots are busy — waited in the foreground instead]", maxBackgroundBash)
	}

	startedAt := time.Now()
	var bgC <-chan time.Time
	if bgAfterSec > 0 {
		bgTimer := time.NewTimer(time.Duration(bgAfterSec) * time.Second)
		defer bgTimer.Stop()
		bgC = bgTimer.C
	}
	deadline := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer deadline.Stop()

	// Someone typing cuts the foreground wait short. The command is not
	// touched — it moves to the background exactly as it would at 30s — so
	// nothing is lost, and the question gets answered instead of queueing
	// behind work the person did not ask to wait for. Only when the handoff is
	// possible at all: with no free slot there is nowhere to move it to, and
	// abandoning the command would be worse than a slow reply.
	var pollC <-chan time.Time
	if bgAfterSec > 0 {
		poll := time.NewTicker(userPendingPoll)
		defer poll.Stop()
		pollC = poll.C
	}

	for {
		select {
		case waitErr := <-run.exit:
			output := strings.TrimRight(run.out.result(), "\n")
			if waitErr != nil {
				return ToolResult{Type: "result", Success: false, Error: formatExitError(waitErr, output) + note}
			}
			return ToolResult{Type: "result", Success: true, Content: output + note}

		case <-pollC:
			if !UserPending() {
				continue
			}
			if res, ok := t.handoff(ctx, run, label, int(time.Since(startedAt).Seconds())); ok {
				return res
			}
			// No slot free: stop asking, and let the normal timers decide.
			pollC = nil

		case <-bgC:
			// One shot: if the handoff is refused (no free slot) we keep
			// waiting in the foreground rather than retrying in a loop.
			bgC = nil
			if res, ok := t.handoff(ctx, run, label, bgAfterSec); ok {
				return res
			}

		case <-deadline.C:
			run.kill()
			<-run.exit
			return ToolResult{
				Type:    "result",
				Success: false,
				Error:   fmt.Sprintf("bash tool timed out after %ds (process killed). If it legitimately needs longer, re-run it with run_in_background=true — you will be notified when it finishes — or raise timeout. Partial output:\n%s", timeoutSec, strings.TrimRight(run.out.result(), "\n")),
			}

		case <-ctx.Done():
			run.kill()
			<-run.exit
			if ctx.Err() == context.DeadlineExceeded {
				return ToolResult{Type: "result", Success: false, Error: "bash tool timed out"}
			}
			return ToolResult{Type: "result", Success: false, Error: "bash tool cancelled"}
		}
	}
}

// handoff moves an already-running command to the background: it registers a
// job that owns the rest of the command's life, and returns the message the
// model gets in place of the command's output. ok is false when the handoff
// could not happen (no JobStarter wired, or every background slot busy), in
// which case nothing has changed and the caller keeps waiting.
//
// The context surgery here is the crux of the whole feature. The ctx passed
// to a tool call is cancelled the moment the call returns (see
// agent/tools_exec.go), and the bash tool kills its process group on
// cancellation — so returning early on the caller's context would kill the
// very command we are trying to keep alive. context.WithoutCancel severs
// that link and WithTimeout puts back a backstop the job itself owns; the
// same detach-and-backstop pattern the async subagent spawn path uses.
func (t *BashTool) handoff(ctx context.Context, run *bashRun, label string, waited int) (ToolResult, bool) {
	if jobStarter == nil {
		return ToolResult{}, false
	}
	if !acquireBackgroundSlot() {
		return ToolResult{}, false
	}

	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), BashBackgroundLimitSec*time.Second)

	// Stop streaming into the tool block before anyone can observe the
	// handoff: the block belongs to a tool call that is about to return, and
	// painting into it afterwards would write into finished output.
	run.handed.Store(true)

	// registered gates the job goroutine until this function has recorded the
	// job in the background registry. Without it the two race: Start may run
	// fn to completion before we register, and fn's deferred unregister would
	// then run first — leaving a dead id behind that kill_job would happily
	// "succeed" on, and a leaked background slot. Registering here rather
	// than inside fn is what makes kill_job usable on the very next tool
	// call, which is exactly when a model that mistyped a command reaches for
	// it.
	registered := make(chan struct{})

	handle := jobStarter.Start(jobCtx, "bash: "+label, func(jobCtx context.Context, jobID string) (string, bool, error) {
		<-registered
		defer cancel()
		defer releaseBackgroundSlot()
		defer unregisterBackgroundBash(jobID)
		run.setJobID(jobID)

		// started is when the command began, not when it was backgrounded, so
		// the notices below report the age a person would recognise.
		started := time.Now().Add(-time.Duration(waited) * time.Second)
		waitErr, killed := watchBackgroundRun(jobCtx, run, jobID, label, started,
			BashFirstProgressNoticeSec*time.Second, BashProgressNoticeEverySec*time.Second)

		output := strings.TrimRight(run.out.result(), "\n")
		notify(bashNotice(jobID, label, waitErr, killed, run.out.total()))

		switch {
		case killed:
			return output, false, fmt.Errorf("background command was stopped before it finished (kill_job, or the %ds background limit). Partial output:\n%s", BashBackgroundLimitSec, output)
		case waitErr != nil:
			return output, false, errors.New(formatExitError(waitErr, output))
		default:
			return output, false, nil
		}
	})

	// cancel() is the kill switch for both paths that can stop this command
	// early — the BashBackgroundLimitSec backstop and an explicit kill_job —
	// because the select in the job goroutine turns a done context into an
	// actual process-group kill.
	registerBackgroundBash(handle.ID(), cancel)
	close(registered)

	waitedNote := "moved to the background"
	if waited > 0 {
		waitedNote = fmt.Sprintf("still running after %ds, so it was moved to the background", waited)
	}
	return ToolResult{
		Type:    "result",
		Success: true,
		Content: fmt.Sprintf(
			"Command %s as job_id=%q (%s).\n\n"+
				"Do NOT run this command again — it is still running, and a second copy would race with the first. "+
				"Do NOT call wait for it either: a notice reaches you the moment it finishes, and wait before then just "+
				"burns a turn to be told what you already know. The one thing to do now is other work that does not "+
				"depend on this result; read the output with wait(job_id=%q) after you are told, or stop it early with "+
				"kill_job(job_id=%q).",
			waitedNote, handle.ID(), label, handle.ID(), handle.ID(),
		),
	}, true
}

// bashNotice is the one line the model sees when a background command
// finishes. Deliberately a summary and not the output: the notice is injected
// into the conversation without the model asking for it, and it stays in the
// history for the rest of the session, so the bytes have to be worth it. The
// output itself stays in the job registry, one wait() call away.
func bashNotice(jobID, label string, waitErr error, killed bool, outputBytes int64) string {
	var outcome string
	switch {
	case killed:
		outcome = "was stopped before finishing"
	case waitErr != nil:
		code := -1
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code = exitErr.ExitCode()
		}
		if hint := exitCodeHint(code); hint != "" {
			outcome = fmt.Sprintf("failed: exit %d (%s)", code, hint)
		} else if code >= 0 {
			outcome = fmt.Sprintf("failed: exit %d", code)
		} else {
			outcome = "failed: " + waitErr.Error()
		}
	default:
		outcome = "finished: exit 0"
	}
	return fmt.Sprintf(
		"[background command] %s — %s (%s of output). Read the output with wait(job_id=%q) if you still need it; ignore this notice if you don't.",
		label, outcome, humanBytes(outputBytes), jobID,
	)
}

func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// bgProgressInterval throttles the progress notes a backgrounded command
// posts to the jobs panel. Every note publishes an event on the job event bus
// and repaints the panel, so forwarding every line of a chatty build would
// turn the TUI into a redraw loop for output nobody is reading line by line.
const bgProgressInterval = time.Second

// bashRun owns one running shell command: the process, the capped output
// buffer both the foreground and the background path read from, and the
// single exit signal that decides which of them gets the result.
type bashRun struct {
	cmd  *exec.Cmd
	out  *cappedBuffer
	exit chan error // exactly one send, after all output has been drained

	// handed flips to true when the command is moved to the background. The
	// output pump reads it to decide where a line goes: the live tool block
	// (foreground) or a throttled progress note (background).
	handed atomic.Bool

	mu           sync.Mutex
	jobID        string
	lastProgress time.Time
}

func (r *bashRun) setJobID(id string) {
	r.mu.Lock()
	r.jobID = id
	r.mu.Unlock()
}

// setProgress posts a throttled progress note for a backgrounded command, so
// the jobs panel shows what it is doing rather than just "running".
func (r *bashRun) setProgress(line string) {
	reporter := jobProgressReporter
	if reporter == nil {
		return
	}
	r.mu.Lock()
	id := r.jobID
	if id == "" || time.Since(r.lastProgress) < bgProgressInterval {
		r.mu.Unlock()
		return
	}
	r.lastProgress = time.Now()
	r.mu.Unlock()

	reporter.SetProgress(id, truncateLine(line, 120))
	// Backgrounded bash output is as much a sign of life as a streamed
	// token — route it through the same touch-point streamingCollector uses
	// (see activity.go) so the jobs panel doesn't read a busy shell command
	// as quiet just because it never calls report_progress.
	touchJobActivity(id)
}

// kill terminates the whole process group. Setpgid at start time is what
// makes this reach the command's children too — killing only the bash pid
// would leave an orphaned compiler or test runner behind.
func (r *bashRun) kill() {
	if r.cmd.Process != nil {
		_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// startBash launches the command and wires up output collection. It returns
// as soon as the process has started; run.exit reports the outcome later.
//
// The two modes differ only in how output reaches the buffer: with a
// streaming callback in context we read the pipes line by line (so the TUI
// can show progress live), otherwise the buffer is wired straight to
// stdout/stderr. In both cases the retained bytes are bounded by
// cappedBuffer, and the exit signal is sent only after the output is fully
// drained — so whoever reads run.out after receiving from run.exit sees
// everything the command wrote.
func startBash(ctx context.Context, cmdVal string) (*bashRun, error) {
	c := exec.Command("bash", "-c", cmdVal)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A child agent working in its own git worktree runs its commands there.
	// Empty means the process directory, which is exec's default anyway. See
	// tools/workdir.go.
	c.Dir = Workdir(ctx)

	stdin, _ := c.StdinPipe()
	if stdin != nil {
		stdin.Close()
	}

	run := &bashRun{
		cmd:  c,
		out:  newCappedBuffer(bashHeadMax, bashTailMax),
		exit: make(chan error, 1),
	}

	onLine := stream.Output(ctx)
	if onLine == nil {
		c.Stdout = run.out
		c.Stderr = run.out
		if err := c.Start(); err != nil {
			return nil, err
		}
		go func() { run.exit <- c.Wait() }()
		return run, nil
	}

	toolIdx := 0
	if idx, ok := ctx.Value(stream.ToolIdxCtxKey{}).(int); ok {
		toolIdx = idx
	}

	stdoutPipe, err := c.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := c.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := c.Start(); err != nil {
		return nil, err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	pump := func(r io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = run.out.Write([]byte(line))
			_, _ = run.out.Write(newline)
			if run.handed.Load() {
				run.setProgress(line)
				continue
			}
			onLine(toolIdx, line)
		}
	}
	go pump(stdoutPipe)
	go pump(stderrPipe)
	go func() {
		wg.Wait() // both pipes drained into run.out before we report the exit
		run.exit <- c.Wait()
	}()

	return run, nil
}

// watchBackgroundRun waits for a backgrounded command, emitting a "still
// running" notice after first and then every every.
//
// The durations are parameters rather than the constants directly so a test
// can drive the whole schedule in milliseconds. Returns the command's exit
// error and whether it was killed, exactly as the plain select it replaced.
func watchBackgroundRun(jobCtx context.Context, run *bashRun, jobID, label string, started time.Time, first, every time.Duration) (error, bool) {
	// A command that was already older than the first interval when it got
	// here (a long background_after, say) is due for its notice immediately
	// rather than skipping it.
	due := first - time.Since(started)
	if due < 0 {
		due = 0
	}
	timer := time.NewTimer(due)
	defer timer.Stop()

	sent := 0
	for {
		select {
		case waitErr := <-run.exit:
			return waitErr, false
		case <-jobCtx.Done():
			run.kill()
			return <-run.exit, true
		case <-timer.C:
			sent++
			notify(bashRunningNotice(jobID, label, time.Since(started), sent == 1))
			timer.Reset(every)
		}
	}
}

// bashRunningNotice is the heads-up for a command that is taking a while.
//
// Its whole job is to be ignorable. It does not ask for anything, because the
// case it is aimed at — a typo that turned a quick command into a hang — is
// indistinguishable from a legitimately slow build, and only the model knows
// which it wrote. Telling it to stop and re-check would interrupt real work
// most of the time it fired; saying how long it has been running lets the
// model recognise the wrong one on its own.
func bashRunningNotice(jobID, label string, ran time.Duration, first bool) string {
	if first {
		return fmt.Sprintf(
			"[background command] %s has been running for %s (job_id=%q). "+
				"For information only — no action needed, and nothing is wrong with it. "+
				"If you expected it to be quick, that is worth knowing; if you expected it to take a while, ignore this. "+
				"You will still be told when it finishes.",
			label, humanDuration(ran), jobID)
	}
	return fmt.Sprintf(
		"[background command] %s is still running, now %s in (job_id=%q). Still just information — no action needed.",
		label, humanDuration(ran), jobID)
}

// humanDuration rounds a duration to something readable in a one-line notice.
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	}
	// Truncated, not rounded: "has been running for 1m" at 90 seconds is
	// honest, while "2m" overstates it, and this notice is only useful if the
	// number can be trusted at a glance.
	m := int(d.Minutes())
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}
