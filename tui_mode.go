package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/crazy-goat/tyci-agent/conductor"
	"github.com/crazy-goat/tyci-agent/display"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/crazy-goat/tyci-agent/tools"
)

// slashCommandDisplay is the narrow slice of *display.TUI that
// handleBareResumeCommand/handleResumeAllCommand/handleBareBtwCommand/
// handleBtwQuestionCommand/handleMsgSlashCommand/handleCompactCommand
// actually call. Extracted so item 37's fix — every one of them must call
// ResetStatus() on every exit path, since none of them ever runs a real
// agent turn and nothing else would restore TuiModel.reading — can be
// pinned with a fake in tests instead of runTUI's real *display.TUI, which
// drives a live bubbletea Program and isn't practically unit-testable.
type slashCommandDisplay interface {
	Error(err error)
	ToolBlock(msg string)
	ResetStatus()
	OpenResumePicker(entries []display.TuiResumeEntry)
	OpenBtwList()
}

// handleBareResumeCommand implements bare "/resume": list cwd's sessions in
// the popup picker. Calls disp.ResetStatus() before doing anything else —
// opening a picker (or failing to) never runs the agent, so nothing else on
// any exit path would restore reading.
func handleBareResumeCommand(disp slashCommandDisplay, wd string, resumeEntries func(string) ([]session.ResumeEntry, error)) {
	disp.ResetStatus()
	entries, err := resumeEntries(wd)
	if err != nil {
		disp.Error(fmt.Errorf("/resume: %v", err))
		return
	}
	if len(entries) == 0 {
		dir, _ := session.SessionDir(wd)
		disp.ToolBlock(fmt.Sprintf("ℹ️  No sessions in %s", dir))
		return
	}
	disp.OpenResumePicker(resumeEntriesToTUI(entries))
}

// handleResumeAllCommand implements "/resume --all" — same shape as
// handleBareResumeCommand, across every project instead of just cwd.
func handleResumeAllCommand(disp slashCommandDisplay, resumeEntriesAll func() ([]session.ResumeEntry, error)) {
	disp.ResetStatus()
	entries, err := resumeEntriesAll()
	if err != nil {
		disp.Error(fmt.Errorf("/resume --all: %v", err))
		return
	}
	if len(entries) == 0 {
		disp.ToolBlock("ℹ️  No sessions recorded")
		return
	}
	disp.OpenResumePicker(resumeEntriesToTUI(entries))
}

// handleBareBtwCommand implements bare "/btw": browse previous side
// conversations. The list popup has no success-path equivalent of
// resumeSession's Reset() — nothing else restores reading on any exit
// (Esc, viewing an entry, or nothing at all).
func handleBareBtwCommand(disp slashCommandDisplay) {
	disp.ResetStatus()
	disp.OpenBtwList()
}

// handleBtwQuestionCommand implements "/btw <question>": forks the current
// conversation into an INDEPENDENT background side conversation (its own
// sink, its own modal) via spawn — it never produces a "done" for the main
// conversation, so without ResetStatus here the main prompt would stay
// stuck refusing input for as long as the side conversation takes to close.
func handleBtwQuestionCommand(disp slashCommandDisplay, question string, spawn func(string)) {
	question = strings.TrimSpace(question)
	if question == "" {
		disp.Error(fmt.Errorf("/btw: question required"))
		disp.ResetStatus()
		return
	}
	disp.ResetStatus()
	spawn(question)
}

// handleMsgSlashCommand implements "/msg <job> <text>": posts to a job's
// mailbox. Same class of bug as the two commands above — this never runs
// the agent, so nothing else would restore reading.
func handleMsgSlashCommand(disp slashCommandDisplay, arg string, post func(string)) {
	disp.ResetStatus()
	post(arg)
}

// handleCompactCommand implements "/compact [focus]". compact does the
// actual work (empty-history/no-session checks, cond.Compact) and returns
// (resultMessage, isError) for disp to render — kept a plain func so this
// stays testable without a real *conductor.Conductor. Same class of bug as
// the commands above: compaction never runs the agent, so nothing else
// would restore reading — but UNLIKE the other five handlers, compact() can
// be a genuinely slow, synchronous model round-trip, so ResetStatus() runs
// AFTER it returns, not before: resetting first would flip the status bar
// to idle (and let a prompt typed mid-compaction go straight to the
// transcript instead of the pending-message queue) for the whole duration
// of the compaction call, which is exactly the busy state ResetStatus is
// supposed to end, not start.
func handleCompactCommand(disp slashCommandDisplay, compact func() (string, bool)) {
	msg, isErr := compact()
	disp.ResetStatus()
	if isErr {
		disp.Error(errors.New(msg))
		return
	}
	if msg != "" {
		disp.ToolBlock(msg)
	}
}

// compactionDisplay is the part of display.TUI that shows a compaction divider.
type compactionDisplay interface {
	Compaction(meta session.CompactMeta)
}

// compactAndShow runs compact and, when it succeeds, shows the divider of
// meta on disp. A failed compaction shows no divider.
func compactAndShow(disp compactionDisplay, compact func(summary, focus string, meta session.CompactMeta) (string, error), summary, focus string, meta session.CompactMeta) (string, error) {
	path, err := compact(summary, focus, meta)
	if err == nil {
		disp.Compaction(meta)
	}
	return path, err
}

// runTUI is the full-screen frontend. It reads user input, dispatches slash
// commands and paints; the conversation behind it — history, model client,
// session log, usage — is the conductor's.
func runTUI(cond *conductor.Conductor, tuiDisp *display.TUI, baseCtx context.Context) {
	titleSet := false // track whether terminal title has been set

	// "/<name>" starts a workflow from the input (display/tui_workflow.go). A
	// workflow named like a builtin command is an error, shown once here.
	tuiDisp.SetWorkflowStarter(workflowStarter)
	for _, err := range reservedWorkflowErrors(workflowStarter.List()) {
		tuiDisp.Error(err)
	}

	// Replay session history if resuming. We use the stable block-per-
	// message replay path so the transcript IS visible (user wanted to
	// scroll it) but selection + scroll stay sane on long sessions:
	// renderErrorOrBlock (no glamour) keeps cachedLines deterministic,
	// and per-message blocks let the existing scroll heuristics handle
	// pagination correctly.
	resumed := false
	if sess := cond.Session(); sess != nil && sess.IsResume() && cond.SessionPath() != "" {
		resumed = true
		parsedLines := sess.Messages()
		rebuiltMsgs, _ := session.RebuildMessages(parsedLines)
		if len(rebuiltMsgs) > 0 {
			cond.SetHistory(rebuiltMsgs)
		}
		replaySessionToDisplay(tuiDisp, cond.SessionPath())
	}
	if cond.SystemPromptDrift() {
		fmt.Fprintln(os.Stderr, "Note: this session's system prompt has changed since it last ran (tools or prompt updated).")
	}

	// The start-up greeting and the orchestrator: display only, never in the
	// model history. Posting from the orchestrator goroutine is safe.
	startTUIOrchestrator(baseCtx, resumed, func(line string) { tuiDisp.Text(line + "\n") })

	// A person typing must not have to wait for whatever is running. Tools that
	// can hand their work to the background check this and do so at once; the
	// work itself is untouched, only the waiting ends. See tools.SetUserPending.
	tools.SetUserPending(tuiDisp.HasPendingMessages)
	defer tools.SetUserPending(nil)

	// Close TUI on exit, write session end
	defer func() {
		// Background shell commands are deliberately detached from both the
		// tool call and the session context (see tools.BashTool.handoff), so
		// nothing else would reap them. A build still running after the
		// session that started it has ended is a surprise, not a feature.
		tools.KillAllBackgroundBash()
		cond.EndSession("ok", 0)
		tuiDisp.Close()
	}()

	// resumeSession swaps the running session + conversation onto a previously-
	// recorded JSONL file. Used both by the slash command (with an explicit path
	// arg) and after a successful pick from the /resume popup. Errors surface
	// to the TUI as error blocks; the active iteration is cancelled after the
	// swap so the next prompt writes to the *resumed* session rather than the
	// abandoned one. The conductor owns this step, so /resume does not
	// reimplement it.
	resumeSession := func(resumePath string, cancellation context.CancelFunc) error {
		summary, msgs, total, corrupt, err := session.LoadForReplay(resumePath)
		if err != nil {
			return fmt.Errorf("load: %w", err)
		}
		if len(corrupt) > 0 {
			tuiDisp.ToolBlock(fmt.Sprintf("⚠️  %d corrupt lines skipped", len(corrupt)))
		}

		// Conductor.Resume closes the current session cleanly before
		// swapping so we don't leak its file handle or write a session_end
		// twice on exit — including the ordering trap that the session_end
		// event has to be written BEFORE Close(), because WriteSessionEnd
		// refuses to encode into a closed writer and the outer defer will
		// try again on process exit.
		if err := cond.Resume(resumePath, msgs, stream.Usage{
			Input:     total.Input,
			Output:    total.Output,
			Reasoning: total.Reasoning,
			CacheRead: total.CacheRead,
		}); err != nil {
			return fmt.Errorf("reopen: %w", err)
		}

		// Drop the in-flight iteration — its context is no longer relevant
		// since the conversation it was going to write to just changed.
		if cancellation != nil {
			cancellation()
		}
		// Render the swapped-in transcript as a fresh stream of stable
		// blocks (one ToolBlock per message). The user picked this
		// session explicitly, so the deterministic no-glamour rendering
		// keeps scrolling and mouse selection working.
		tuiDisp.Reset()
		replaySessionToDisplay(tuiDisp, resumePath)
		fmt.Fprintf(os.Stderr, "ℹ Resumed session %s (%d messages)\n", summary.ID, len(msgs))
		if cond.SystemPromptDrift() {
			fmt.Fprintln(os.Stderr, "Note: this session's system prompt has changed since it last ran (tools or prompt updated).")
		}
		return nil
	}

	cond.SetCompactor(func(summary, focus string, meta session.CompactMeta) (string, error) {
		return compactAndShow(tuiDisp, cond.Compact, summary, focus, meta)
	})

	// handleMsgCommand implements "/msg <job> <text>": posts text to job's
	// mailbox, delivered at that job's next iteration boundary (see
	// tools.JobMailboxNextMessages) — the human-facing equivalent of the
	// "message" tool. Parsing/resolution lives in parseMsgCommand/
	// postMsgCommand (package-level, unit-testable without a TUI); this
	// closure only adapts the result to tuiDisp.Error/ToolBlock, since both
	// call sites below (busy-turn drain and the idle loop) just
	// fire-and-forget it.
	handleMsgCommand := func(arg string) {
		if jobID, err := postMsgCommand(appBus, JobRegistry, arg); err != nil {
			tuiDisp.Error(err)
		} else {
			tuiDisp.ToolBlock(fmt.Sprintf("ℹ️  message queued for job %s", jobID))
		}
	}

	// startBtwQuestion forks the conversation into a background side
	// conversation. Runs on baseCtx (not the per-iteration context the loop
	// below cancels) so it keeps going independently of the main thread.
	// open adds the entry to the /btw list, and opens its modal only for a /btw
	// command. A busy-line fork only records the entry, so the prompt keeps the keyboard.
	openBtwEvaluation := func(question string, start func(context.Context, *conductor.Conductor, string, *display.BtwSink) *jobs.Job, open func(id, question string)) {
		id := nextBtwID()
		sink := tuiDisp.BtwSink(id)
		open(id, question)
		job := start(baseCtx, cond, question, sink)
		if job == nil {
			tuiDisp.Error(btwLimitError())
			return
		}
		tuiDisp.SetBtwJobID(id, job.ID)
	}
	startBtwQuestion := func(question string) { openBtwEvaluation(question, startBtw, tuiDisp.OpenBtw) }

	// serviceCommands runs the slash commands typed while a turn was in
	// flight, when the main loop below was blocked in the agent run and could
	// not read them (see display.TUI.Commands). It is installed as part of
	// NextMessages, so it runs on the agent's goroutine between iterations —
	// the one point where reading the conversation is safe, which /btw's fork
	// needs. It contributes no messages: a side conversation is deliberately
	// invisible to the main one.
	serviceCommands := func() []string {
		for _, cmd := range tuiDisp.DrainCommands() {
			switch {
			case cmd == "/btw":
				tuiDisp.OpenBtwList()
			case strings.HasPrefix(cmd, "/btw "):
				question := strings.TrimSpace(strings.TrimPrefix(cmd, "/btw"))
				if question == "" {
					tuiDisp.Error(fmt.Errorf("/btw: question required"))
					continue
				}
				startBtwQuestion(question)
			case strings.HasPrefix(cmd, "/msg "):
				handleMsgCommand(strings.TrimSpace(strings.TrimPrefix(cmd, "/msg")))
			}
		}
		return nil
	}
	// Keep completion notices visible in the TUI as well as delivering them
	// to the model. Previously the notice queue was wired only as a silent
	// NextMessages source, so a child could finish successfully while the
	// person saw no indication until they inferred it from model output.
	drainOrchestratorNotices := func() []string {
		notices, shown := drainNoticesForTUI()
		for _, notice := range shown {
			tuiDisp.ToolBlock(notice)
		}
		return notices
	}
	// The queue drain is the busy-line path: a line typed while a turn is in
	// flight waits in the queue. forkBusyLines starts a read-only fork for it
	// and the line still goes to the orchestrator.
	startBusyQuestion := func(question string) { openBtwEvaluation(question, startBusyBtw, tuiDisp.RecordBtw) }
	cond.SetNextMessages(mergeNextMessages(serviceCommands, forkBusyLines(cond.Config().NextMessages, startBusyQuestion), drainOrchestratorNotices))

	for {
		iterCtx, iterCancel := context.WithCancel(baseCtx)

		// Wait for user input, model change, /resume selection, or a
		// background command finishing.
		var line string
		select {
		case <-busOrchestratorNotices.Ready():
			notices := wakeNotices()
			if len(notices) == 0 {
				iterCancel()
				continue
			}
			line = strings.Join(notices, "\n")

		case l, ok := <-tuiDisp.Results():
			if !ok {
				iterCancel()
				return
			}
			line = l

		case resumePath, ok := <-tuiDisp.SelectedResume():
			iterCancel()
			if !ok {
				// Channel closed without a value — only happens if the TUI
				// is shutting down. Quit the loop to be safe.
				return
			}
			if resumePath == "" {
				// User pressed Esc in the picker. Stay in the loop; do nothing.
				continue
			}
			if err := resumeSession(resumePath, iterCancel); err != nil {
				tuiDisp.Error(err)
				tuiDisp.ResetStatus()
				continue
			}
			continue

		case <-tuiDisp.DoneCh():
			iterCancel()
			return
		}

		rawLine := line
		trimmed := strings.TrimSpace(line)
		if rawLine != "" && !strings.HasPrefix(rawLine, " ") && strings.HasPrefix(trimmed, "/") {
			// Slash commands: raw input starts with "/" (no leading space).
			arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/resume"))
			switch {
			case trimmed == "/exit":
				iterCancel()
				return
			case trimmed == "/compact" || strings.HasPrefix(trimmed, "/compact "):
				// Deliberately cancel the current iteration before changing its live
				// history; compaction is a conversation boundary, not a queued prompt.
				iterCancel()
				focus := strings.TrimSpace(strings.TrimPrefix(trimmed, "/compact"))
				handleCompactCommand(tuiDisp, func() (string, bool) {
					if len(cond.Messages()) == 0 {
						// Nothing to compact yet: manualCompactSummary is never
						// empty, so without this check a bare /compact on a
						// fresh session would still create a session file and a
						// dump for no reason.
						return "/compact: nothing to compact yet", true
					}
					sess := cond.EnsureSession()
					if sess == nil {
						return "/compact: no writable session", true
					}
					// DumpPathFor is deterministic, so the real path can be
					// folded into the summary that becomes the compacted
					// history's lead message — not just printed here — before
					// Compact ever writes it.
					dumpPath := session.DumpPathFor(cond.SessionPath())
					path, err := compactAndShow(tuiDisp, cond.Compact, manualCompactSummary(dumpPath), focus, session.CompactMeta{Kind: session.CompactKindCommand, At: time.Now()})
					if err != nil {
						return fmt.Sprintf("/compact: %v", err), true
					}
					return "History compacted; raw record: " + path, false
				})
				continue
			case trimmed == "/new":
				iterCancel()
				// Stop all async work from the old conversation before clearing
				// its UI. Waiting for completion prevents terminal events and
				// notices from being delivered into the new conversation.
				oldJobIDs := JobRegistry.CancelAll()
				clearNotices()
				tuiDisp.ResetJobs(oldJobIDs)
				// Cleanly terminate the live session so /new doesn't leave
				// the file open with no closing event. /resume rebuilds
				// later, so we need a proper boundary here. The TUI also zeroes the
				// usage total, because the next prompt starts a fresh log.
				cond.EndSession("ok", 0)
				cond.ClearHistory()
				cond.ResetUsage()
				tools.ClearTodoList()
				tuiDisp.Reset()
				fmt.Fprint(os.Stdout, ansi.SetWindowTitle("tyci"))
				titleSet = false
				continue
			case trimmed == "/resume":
				// Bare /resume: list cwd's sessions in the popup picker.
				iterCancel()
				wd, _ := os.Getwd()
				handleBareResumeCommand(tuiDisp, wd, session.ResumeEntries)
				continue
			case trimmed == "/resume --all":
				// Escape hatch: list sessions across every project, not
				// just the one containing cwd.
				iterCancel()
				handleResumeAllCommand(tuiDisp, session.ResumeEntriesAll)
				continue
			case trimmed == "/btw":
				// Bare /btw: browse previous side-conversations from this session.
				iterCancel()
				handleBareBtwCommand(tuiDisp)
				continue
			case strings.HasPrefix(trimmed, "/btw "):
				// /btw <question>: fork the current conversation into a
				// background side-conversation. Runs on baseCtx (not
				// iterCtx, which the top of this loop cancels every
				// iteration) so it keeps going independently of the main
				// thread's turns.
				iterCancel()
				handleBtwQuestionCommand(tuiDisp, strings.TrimPrefix(trimmed, "/btw"), startBtwQuestion)
				continue
			case strings.HasPrefix(trimmed, "/msg "):
				// /msg <job> <text>: posts to a job's mailbox. Doesn't touch
				// the conversation this iteration's turn would write to, but
				// this iteration never starts one either, so iterCtx is
				// cancelled like every other command below that doesn't run
				// the agent.
				iterCancel()
				handleMsgSlashCommand(tuiDisp, strings.TrimSpace(strings.TrimPrefix(trimmed, "/msg")), handleMsgCommand)
				continue
			case strings.HasPrefix(trimmed, "/resume "):
				// /resume <path|index>: forward to resolveSessionRef so the
				// caller can pass either a file path or a numeric 1-based
				// index into the session list (matching the cobra CLI).
				iterCancel()
				path, err := resolveSessionRef(".", arg)
				if err != nil {
					tuiDisp.Error(fmt.Errorf("/resume: %v", err))
					tuiDisp.ResetStatus()
					continue
				}
				if err := resumeSession(path, iterCancel); err != nil {
					tuiDisp.Error(fmt.Errorf("/resume: %v", err))
					tuiDisp.ResetStatus()
					continue
				}
				continue
			default:
				cmd := strings.Fields(trimmed)[0]
				tuiDisp.Error(fmt.Errorf("unknown command: %s", cmd))
				tuiDisp.ResetStatus()
				iterCancel()
				continue
			}
		}
		line = trimmed
		if line == "" {
			iterCancel()
			continue
		}

		// Set terminal title on the very first user prompt (and never again
		// until /new resets it). Show "tyci:" followed by the prompt truncated
		// to 32 characters so the tab/window label stays readable.
		if !titleSet {
			title := line
			runes := []rune(line)
			if len(runes) > 32 {
				title = string(runes[:32])
			}
			fmt.Fprint(os.Stdout, ansi.SetWindowTitle("tyci: "+title))
			titleSet = true
		}

		// Run the turn in a goroutine so we can interrupt it via ESC.
		// Submit records the user line, lazily materializes the session file
		// on the first prompt and drives the agent loop; the pending-message
		// queue drain callback (issue #88) is wired into the conductor's
		// agent.Config once, at construction.
		type agentResult struct {
			usage stream.Usage
			err   error
		}
		resultCh := make(chan agentResult, 1)
		go func() {
			u, e := cond.Submit(iterCtx, line)
			resultCh <- agentResult{usage: u, err: e}
		}()

		select {
		case <-tuiDisp.CancelCh():
			// ESC pressed — cancel the agent run
			cond.Interrupt()
			iterCancel()
			// Wait for the agent to finish before painting anything
			// else — it is still writing to the display. The result
			// itself is dropped on purpose: a cancellation is what we
			// just asked for, and a real error has already been shown
			// to the user by agent.Run via d.Error().
			<-resultCh
			// A command typed in the last moments of the turn would otherwise
			// sit in the channel until the next turn's first gap.
			serviceCommands()
			tuiDisp.ResetStatus()
			// User probably wants to retry with a new prompt
			continue

		case res := <-resultCh:
			iterCancel()
			serviceCommands()

			tuiDisp.Done(res.usage, stream.Stats{})

			if res.err != nil && !errors.Is(res.err, context.Canceled) {
				// Error already shown via d.Error() in agent.Run, continue
				continue
			}
		}
	}
}

// resumeEntriesToTUI converts session-package rows to the display-package
// TUI rows. Kept in the main package (not in display) so display doesn't
// pull in session internals — and so a future port that uses an in-memory
// session store can plug in its own adapter without changing the picker.
func resumeEntriesToTUI(entries []session.ResumeEntry) []display.TuiResumeEntry {
	out := make([]display.TuiResumeEntry, len(entries))
	for i, e := range entries {
		out[i] = display.TuiResumeEntry{
			Path:        e.Path,
			Name:        e.Name,
			ModTime:     e.ModTime.Time(),
			FirstPrompt: e.FirstPrompt,
		}
	}
	return out
}

// watchESC starts a goroutine that monitors the terminal for the ESC key (0x1b).
// When ESC is pressed, it calls cancel() to interrupt the current operation.
// It sets stdin to raw+cbreak mode (non-canonical, echo off, ISIG on, OPOST on)
// with VMIN=0 and VTIME=1 (100ms timeout) so the goroutine can exit promptly
// when the context is cancelled externally (e.g. Ctrl+C).
// Returns a cleanup function that restores the original terminal state.
// If stdin is not a terminal, returns a no-op function.
