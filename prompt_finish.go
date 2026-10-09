package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/crazy-goat/tyci-agent/agent"
	"github.com/crazy-goat/tyci-agent/conductor"
	"github.com/crazy-goat/tyci-agent/display"
)

// exitCodeError carries the exit code of a one-shot run up to main. main
// exits with that code, after the defers of runCmd have run, and prints no
// message for it.
type exitCodeError int

func (e exitCodeError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// finishPromptRun turns the outcome of a one-shot turn into what the user
// sees, and returns the exit code. Deciding that is the frontend's half of
// the split: the conductor reports what happened, this decides how it looks.
func finishPromptRun(cond *conductor.Conductor, disp display.Display, err error) int {
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
			cond.EndSession("canceled", 130)
			printSessionPath(sessionPath)
			return 130
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		status = "error"
		exitCode = 1
	}
	cond.EndSession(status, exitCode)

	if err != nil {
		printSessionPath(sessionPath)
		return exitCode
	}
	disp.End()
	printSessionPath(sessionPath)
	return 0
}

func printSessionPath(sessionPath string) {
	if sessionPath != "" {
		fmt.Fprintf(os.Stderr, "📁 Session: %s\n", sessionPath)
	}
}
