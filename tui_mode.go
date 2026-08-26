package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// runTUI is the full-screen frontend. It reads user input, dispatches slash
// commands and paints; the conversation behind it — history, model client,
// session log, usage — is the conductor's.
func runTUI(cond *conductor.Conductor, tuiDisp *display.TUI, baseCtx context.Context) {
	titleSet := false // track whether terminal title has been set

	// Replay session history if resuming. We use the stable block-per-
	// message replay path so the transcript IS visible (user wanted to
	// scroll it) but selection + scroll stay sane on long sessions:
	// renderErrorOrBlock (no glamour) keeps cachedLines deterministic,
	// and per-message blocks let the existing scroll heuristics handle
	// pagination correctly.
	if sess := cond.Session(); sess != nil && sess.IsResume() && cond.SessionPath() != "" {
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

	// updateModel resolves a new model string and points the conversation at
	// it. This is in-memory only for the current TUI process. It does not
	// write agents/config files, and /new must not reset it.
	updateModel := func(newModel string) {
		if err := cond.SwitchModel(newModel); err != nil {
			tuiDisp.SetModel(cond.Model()) // revert TUI display to previous model
		}
	}

	// drainModelChanges applies all queued model changes before running a prompt.
	// This avoids a race where the user picks /model and immediately submits the
	// next prompt while the model change is still buffered.
	drainModelChanges := func() {
		for {
			select {
			case newModel, ok := <-tuiDisp.ModelChanges():
				if !ok {
					return
				}
				updateModel(newModel)
			default:
				return
			}
		}
	}

	// resumeSession swaps the running session + conversation onto a previously-
	// recorded JSONL file. Used both by the slash command (with an explicit path
	// arg) and after a successful pick from the /resume popup. Errors surface
	// to the TUI as error blocks; the active iteration is cancelled after the
	// swap so the next prompt writes to the *resumed* session rather than the
	// abandoned one. Mirrors the console implementation so /resume behaves
	// identically across modes — which is now true because both call the same
	// conductor method rather than reimplementing it.
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

		// model/provider may also be restored if the resumed session used a
		// different one.
		if summary.Provider != "" && summary.Model != "" {
			if err := cond.SwitchModel(summary.Provider + "/" + summary.Model); err == nil {
				tuiDisp.SetModel(cond.Model())
			}
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

	cond.SetCompactor(cond.Compact)

	// handleMsgCommand implements "/msg <job> <text>": posts text to job's
	// mailbox, delivered at that job's next iteration boundary (see
	// tools.JobMailboxNextMessages) — the human-facing equivalent of the
	// "message" tool. Parsing/resolution lives in parseMsgCommand/
	// postMsgCommand (package-level, unit-testable without a TUI); this
	// closure only adapts the result to tuiDisp.Error/ToolBlock, since both
	// call sites below (busy-turn drain and the idle loop) just
	// fire-and-forget it.
	handleMsgCommand := func(arg string) {
		if jobID, err := postMsgCommand(JobRegistry, arg); err != nil {
			tuiDisp.Error(err)
		} else {
			tuiDisp.ToolBlock(fmt.Sprintf("ℹ️  message queued for job %s", jobID))
		}
	}

	// startBtwQuestion forks the conversation into a background side
	// conversation. Runs on baseCtx (not the per-iteration context the loop
	// below cancels) so it keeps going independently of the main thread.
	startBtwQuestion := func(question string) {
		id := nextBtwID()
		sink := tuiDisp.BtwSink(id)
		tuiDisp.OpenBtw(id, question)
		job := startBtw(baseCtx, cond, question, sink)
		if job == nil {
			tuiDisp.Error(fmt.Errorf("/btw: too many active evaluations (limit %d)", maxBtwEvaluations))
			return
		}
		tuiDisp.SetBtwJobID(id, job.ID)
	}

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
	// to the model. Previously JobNotices.Drain was wired only as a silent
	// NextMessages source, so a child could finish successfully while the
	// person saw no indication until they inferred it from model output.
	drainJobNotices := func() []string {
		notices := JobNotices.Drain()
		for _, notice := range notices {
			tuiDisp.ToolBlock(notice)
		}
		return notices
	}
	cond.SetNextMessages(mergeNextMessages(serviceCommands, cond.Config().NextMessages, drainJobNotices))

	for {
		iterCtx, iterCancel := context.WithCancel(baseCtx)

		// Wait for user input, model change, /resume selection, or a
		// background command finishing.
		var line string
		select {
		case <-JobNotices.Signal():
			// A background shell command finished while nobody was running.
			// Nothing will drain the notice queue until the next turn starts,
			// so start one here: this is what turns "the command finished"
			// into the agent actually reacting to it, rather than the user
			// having to type something first.
			//
			// Drain can legitimately come back empty — the same notice may
			// have been picked up by cfg.NextMessages during a turn that was
			// still finishing when the signal fired. Starting a turn with an
			// empty prompt would waste an API call, so we just loop.
			notices := JobNotices.Drain()
			if len(notices) == 0 {
				iterCancel()
				continue
			}
			line = strings.Join(notices, "\n")

		case newModel, ok := <-tuiDisp.ModelChanges():
			iterCancel()
			if ok {
				updateModel(newModel)
			}
			continue

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

		drainModelChanges()

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
				if len(cond.Messages()) == 0 {
					// Nothing to compact yet: manualCompactSummary is never
					// empty, so without this check a bare /compact on a
					// fresh session would still create a session file and a
					// dump for no reason.
					tuiDisp.Error(fmt.Errorf("/compact: nothing to compact yet"))
					continue
				}
				focus := strings.TrimSpace(strings.TrimPrefix(trimmed, "/compact"))
				sess := cond.EnsureSession()
				if sess == nil {
					tuiDisp.Error(fmt.Errorf("/compact: no writable session"))
					continue
				}
				// DumpPathFor is deterministic, so the real path can be
				// folded into the summary that becomes the compacted
				// history's lead message — not just printed here — before
				// Compact ever writes it.
				dumpPath := session.DumpPathFor(cond.SessionPath())
				path, err := cond.Compact(manualCompactSummary(dumpPath), focus)
				if err != nil {
					tuiDisp.Error(fmt.Errorf("/compact: %v", err))
				} else {
					tuiDisp.ToolBlock("History compacted; raw record: " + path)
				}
				continue
			case trimmed == "/new":
				iterCancel()
				// Stop all async work from the old conversation before clearing
				// its UI. Waiting for completion prevents terminal events and
				// notices from being delivered into the new conversation.
				oldJobIDs := JobRegistry.CancelAll()
				JobNotices.Clear()
				tuiDisp.ResetJobs(oldJobIDs)
				// Cleanly terminate the live session so /new doesn't leave
				// the file open with no closing event. /resume rebuilds
				// later, so we need a proper boundary here. Unlike the
				// console, the TUI also zeroes the usage total, because the
				// next prompt starts a fresh log.
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
				entries, err := session.ResumeEntries(wd)
				if err != nil {
					tuiDisp.Error(fmt.Errorf("/resume: %v", err))
					tuiDisp.ResetStatus()
					continue
				}
				if len(entries) == 0 {
					dir, _ := session.SessionDir(wd)
					tuiDisp.ToolBlock(fmt.Sprintf("ℹ️  No sessions in %s", dir))
					continue
				}
				tuiEntries := resumeEntriesToTUI(entries)
				tuiDisp.OpenResumePicker(tuiEntries)
				continue
			case trimmed == "/resume --all":
				// Escape hatch: list sessions across every project, not
				// just the one containing cwd.
				iterCancel()
				entries, err := session.ResumeEntriesAll()
				if err != nil {
					tuiDisp.Error(fmt.Errorf("/resume --all: %v", err))
					tuiDisp.ResetStatus()
					continue
				}
				if len(entries) == 0 {
					tuiDisp.ToolBlock("ℹ️  No sessions recorded")
					continue
				}
				tuiDisp.OpenResumePicker(resumeEntriesToTUI(entries))
				continue
			case trimmed == "/btw":
				// Bare /btw: browse previous side-conversations from this session.
				iterCancel()
				tuiDisp.OpenBtwList()
				continue
			case strings.HasPrefix(trimmed, "/btw "):
				// /btw <question>: fork the current conversation into a
				// background side-conversation. Runs on baseCtx (not
				// iterCtx, which the top of this loop cancels every
				// iteration) so it keeps going independently of the main
				// thread's turns.
				iterCancel()
				question := strings.TrimSpace(strings.TrimPrefix(trimmed, "/btw"))
				if question == "" {
					tuiDisp.Error(fmt.Errorf("/btw: question required"))
					tuiDisp.ResetStatus()
					continue
				}
				startBtwQuestion(question)
				continue
			case strings.HasPrefix(trimmed, "/msg "):
				// /msg <job> <text>: posts to a job's mailbox. Doesn't touch
				// the conversation this iteration's turn would write to, but
				// this iteration never starts one either, so iterCtx is
				// cancelled like every other command below that doesn't run
				// the agent.
				iterCancel()
				handleMsgCommand(strings.TrimSpace(strings.TrimPrefix(trimmed, "/msg")))
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
				tuiDisp.Error(fmt.Errorf("Unknown command: %s", cmd))
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
