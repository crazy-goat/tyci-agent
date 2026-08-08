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

// finishPromptRun turns the outcome of a one-shot turn into what the user
// sees and the process exits with. Deciding that is the frontend's half of
// the split: the conductor reports what happened, this decides how it looks.
func finishPromptRun(cond *conductor.Conductor, disp display.Display, err error) {
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
			cond.EndSession("cancelled", 130)
			printSessionPath(sessionPath)
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		status = "error"
		exitCode = 1
	}
	cond.EndSession(status, exitCode)

	if err != nil {
		printSessionPath(sessionPath)
		os.Exit(exitCode)
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
