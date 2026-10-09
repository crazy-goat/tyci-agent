package main

import (
	"fmt"
	"strings"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/jobs"
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
// resolution the "message" tool's tools.JobMailbox.Resolve uses) and sends
// text to its inbox as agent.message from the user. Returns the resolved full
// job id on success. This is the only producer with OriginHuman. Unlike the
// job producers, it returns a publish error, so the user sees that the
// message was not sent.
func postMsgCommand(b *bus.Bus, reg *jobs.Registry, arg string) (jobID string, err error) {
	jobArg, text, err := parseMsgCommand(arg)
	if err != nil {
		return "", err
	}
	jobID, ok := reg.Resolve(jobArg)
	if !ok {
		return "", fmt.Errorf("/msg: unknown job %q", jobArg)
	}
	if !reg.IsLive(jobID) {
		return "", fmt.Errorf("/msg: job %q not found", jobID)
	}
	if err := publishUserMessage(b, jobID, text); err != nil {
		return "", fmt.Errorf("/msg: %w", err)
	}
	return jobID, nil
}

// publishUserMessage sends text from the person to the mailbox of the running
// job jobID. It is the one producer with OriginHuman: "/msg" and the agent
// view both use it, so the agent reads the text the same way in both cases.
func publishUserMessage(b *bus.Bus, jobID, text string) error {
	_, err := bus.Publish(b, bus.KindAgentMessage, bus.Addr{Type: bus.AddrUser}, agentAddr(jobID),
		bus.OriginHuman, bus.AgentMessage{Text: text})
	return err
}
