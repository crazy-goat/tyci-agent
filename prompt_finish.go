package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/decodo/tyci-agent/agent"
	"github.com/decodo/tyci-agent/display"
	"github.com/decodo/tyci-agent/session"
	"github.com/decodo/tyci-agent/stream"
)

func finishPromptRun(disp display.Display, sess *session.Session, sessionPath string, usage stream.Usage, err error) {
	status := "ok"
	exitCode := 0
	if err != nil {
		if errors.Is(err, context.Canceled) {
			disp.End()
			fmt.Fprint(os.Stdout, "\n")
			agent.WriteSessionEnd(sess, "cancelled", 130, &usage)
			printSessionPath(sessionPath)
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		status = "error"
		exitCode = 1
	}
	agent.WriteSessionEnd(sess, status, exitCode, &usage)

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
