package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/session"
)

// runPrompt executes one non-interactive turn.
//
// Everything that used to make this function a second implementation of the
// conversation loop — appending the user message, opening the session file,
// writing the prompt to it, calling agent.Run, accumulating usage — now lives
// in the conductor. What is left here is genuinely one-shot-CLI business:
// which signals mean "stop", and whether a resumed transcript is replayed in
// full or summarized (it is summarized; a huge session would bury the answer).
//
// cleanup is forwarded to finishPromptRun, which calls it right before an
// os.Exit that would otherwise skip it (and any defer in runCmd along with
// it) — see finishPromptRun's doc comment for why that matters now that
// `tyci run` connects MCP servers.
func runPrompt(cond *conductor.Conductor, disp display.Display, prompt string, ctx context.Context, cleanup func()) {
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	// Keep the signal watcher alive independently of runCtx: the first SIGINT
	// cancels the operation, but a second one must still be consumed until the
	// watcher is explicitly stopped below.
	watchCtx, stopWatch := context.WithCancel(ctx)
	sigCh, sigDone := watchInterrupt(watchCtx, runCancel)
	stopESC := watchESC(runCancel)

	// For one-shot `tyci run --prompt ...` we always have a user prompt,
	// so it's safe — and correct — to materialize the session file here.
	// initCommon leaves the conductor's session nil for auto-generated
	// paths and only opens eagerly for explicit --session (resume). We need
	// the file open before deciding what history to prepend, because that
	// decision reads IsResume() off the session itself.
	sess := cond.EnsureSession()

	if hist := resumeHistory(disp, sess, cond.SessionPath()); len(hist) > 0 {
		cond.SetHistory(hist)
	}

	_, err := cond.Submit(runCtx, prompt)

	stopESC()
	signal.Stop(sigCh)
	stopWatch()
	runCancel()
	<-sigDone

	finishPromptRun(cond, disp, err, cleanup)
}

func watchInterrupt(ctx context.Context, cancel context.CancelFunc) (chan os.Signal, chan struct{}) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	sigDone := make(chan struct{})
	go func() {
		defer close(sigDone)
		for {
			select {
			case <-sigCh:
				cancel()
			case <-ctx.Done():
				return
			}
		}
	}()
	return sigCh, sigDone
}

// resumeHistory rebuilds the transcript of a resumed session so the model
// keeps its context, and tells the user what was loaded. It returns nil when
// there is nothing to resume. The prompt itself is not appended here — that
// is the conductor's job, on Submit.
func resumeHistory(disp display.Display, sess *session.Session, sessionPath string) []connector.Message {
	if sess == nil || !sess.IsResume() {
		return nil
	}
	parsedLines := sess.Messages()
	rebuiltMsgs, err := session.RebuildMessages(parsedLines)
	if err != nil || len(rebuiltMsgs) == 0 {
		return nil
	}
	fmt.Fprintf(os.Stderr, "ℹ Resumed session %s (%d messages) from %s\n", sess.ID(), len(rebuiltMsgs), sessionPath)
	// One-shot run mode: render a compact summary (no full replay). A
	// huge session would otherwise flood the terminal and bury the
	// prompt output in stale scrolling text. The model still sees
	// `rebuiltMsgs` so context is preserved.
	summary, _, total, corrupt, err := session.LoadForReplay(sessionPath)
	if err == nil {
		summarizeResume(disp, summary.ID, rebuiltMsgs, total, len(corrupt))
	} else {
		fmt.Fprintf(os.Stderr, "Warning: cannot summarize session: %v\n", err)
	}
	return rebuiltMsgs
}
