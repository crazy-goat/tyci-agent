package main

import (
	"fmt"
	"strings"

	"github.com/decodo/tyci/jobs"
)

// parseMsgCommand splits "/msg"'s argument (the text after "/msg ", already
// trimmed) into the job reference (first whitespace-separated token — a
// full job id or its jobs-panel short "#N" form) and the message text (the
// rest of the line, trimmed). Kept separate from postMsgCommand so parsing
// alone is testable without a *jobs.Registry.
func parseMsgCommand(arg string) (jobArg, text string, err error) {
	fields := strings.Fields(arg)
	if len(fields) < 2 {
		return "", "", fmt.Errorf("/msg: usage: /msg <job> <text>")
	}
	jobArg = fields[0]
	text = strings.TrimSpace(strings.TrimPrefix(arg, jobArg))
	if text == "" {
		return "", "", fmt.Errorf("/msg: text is required")
	}
	return jobArg, text, nil
}

// postMsgCommand implements "/msg <job> <text>" against reg: resolves job
// (full id or short "#N" form, via jobs.Registry.Resolve — the same
// resolution the "message" tool's tools.JobMailbox.Resolve uses) and posts
// text to its mailbox. Returns the resolved full job id on success.
func postMsgCommand(reg *jobs.Registry, arg string) (jobID string, err error) {
	jobArg, text, err := parseMsgCommand(arg)
	if err != nil {
		return "", err
	}
	jobID, ok := reg.Resolve(jobArg)
	if !ok {
		return "", fmt.Errorf("/msg: unknown job %q", jobArg)
	}
	if !reg.Post(jobID, text) {
		return "", fmt.Errorf("/msg: job %q not found", jobID)
	}
	return jobID, nil
}
