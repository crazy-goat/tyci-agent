package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/display"
)

// exitFunc is os.Exit, indirected so a test can observe an exit (and its
// code) without actually terminating the test binary. Production code
// never overrides it.
var exitFunc = os.Exit

// finishPromptRun turns the outcome of a one-shot turn into what the user
// sees and the process exits with. Deciding that is the frontend's half of
// the split: the conductor reports what happened, this decides how it looks.
//
// cleanup is called immediately before every exitFunc call below, and ONLY
// there -- the final, no-error fallthrough at the bottom returns normally,
// so runCmd's own `defer shutdown()` (commands.go) covers that path
// already. os.Exit terminates the process before any deferred func gets to
// run, so without this, a `tyci run` that errors or is interrupted (the
// two cases that reach exitFunc here) would skip tools.ShutdownMCP()
// entirely -- silently leaking a connected MCP server's process on every
// failed or canceled run, cron's included, since cron just shells out to
// `tyci run`. nil is fine: callers that never connected MCP pass nil (this
// file has no import on "tools" to keep the two concerns separate; the
// caller decides what cleanup means).
func finishPromptRun(cond *conductor.Conductor, disp display.Display, err error, cleanup func()) {
	runCleanup := func() {
		if cleanup != nil {
			cleanup()
		}
	}
	sessionPath := cond.SessionPath()
	status := "ok"
	exitCode := 0
	if err != nil && errors.Is(err, agent.ErrMaxIterations) {
		// Iteration-cap stop: warning already shown by agent.Run. Finish
		// normally (exit 0) rather than reporting it as a hard error.
		err = nil
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			disp.End()
			fmt.Fprint(os.Stdout, "\n")
			cond.EndSession("canceled", 130)
			printSessionPath(sessionPath)
			runCleanup()
			exitFunc(130)
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		status = "error"
		exitCode = 1
	}
	cond.EndSession(status, exitCode)

	if err != nil {
		printSessionPath(sessionPath)
		runCleanup()
		exitFunc(exitCode)
		return
	}
	disp.End()
	fmt.Fprintln(os.Stdout)
	printSessionPath(sessionPath)
}

func printSessionPath(sessionPath string) {
	if sessionPath != "" {
		fmt.Fprintf(os.Stderr, "📁 Session: %s\n", sessionPath)
	}
}
