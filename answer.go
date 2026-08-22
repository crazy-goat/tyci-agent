package main

import (
	"fmt"
	"strings"

	"github.com/decodo/tyci/jobs"
)

// waitingJobs returns the jobs on reg currently in jobs.StatusWaitingAnswer
// — the set "/answer" resolves against.
func waitingJobs(reg *jobs.Registry) []jobs.Job {
	var out []jobs.Job
	for _, j := range reg.List() {
		if j.Status == jobs.StatusWaitingAnswer {
			out = append(out, j)
		}
	}
	return out
}

// waitingIDList renders the short ids of waiting, for error messages.
func waitingIDList(waiting []jobs.Job) string {
	ids := make([]string, len(waiting))
	for i, j := range waiting {
		ids[i] = "#" + jobs.ShortID(j.ID)
	}
	return strings.Join(ids, ", ")
}

// resolveAnswerTarget resolves a "/answer" command's argument against the
// jobs currently waiting for an answer. Accepts:
//
//   - "<id> <text>" — id is the short suffix the jobs panel displays (e.g.
//     "3" or "#3"), or the job's full id.
//   - "<text>" alone — allowed only when exactly one job is waiting, which
//     is the common case; with more than one waiting, an id is required.
//
// Returns either (jobID, text) or a user-facing errMsg — never both.
func resolveAnswerTarget(waiting []jobs.Job, arg string) (jobID, text, errMsg string) {
	arg = strings.TrimSpace(arg)
	if len(waiting) == 0 {
		return "", "", "no job is currently waiting for an answer"
	}
	if arg == "" {
		return "", "", "usage: /answer [id] <answer text>"
	}

	fields := strings.Fields(arg)
	head := strings.TrimPrefix(fields[0], "#")
	var matches []jobs.Job
	for _, j := range waiting {
		if head == j.ID || head == jobs.ShortID(j.ID) {
			matches = append(matches, j)
		}
	}
	switch len(matches) {
	case 1:
		rest := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))
		if rest == "" {
			return "", "", "usage: /answer [id] <answer text>"
		}
		return matches[0].ID, rest, ""
	case 0:
		if len(waiting) == 1 {
			// No id matched, but there's nothing to disambiguate: treat
			// the whole argument as the answer text for the one waiting job.
			return waiting[0].ID, arg, ""
		}
		return "", "", fmt.Sprintf("multiple jobs waiting (%s) — specify one: /answer <id> <text>", waitingIDList(waiting))
	default:
		// Should not happen in practice (short ids are unique for the life
		// of the process), but resolve unambiguously rather than guessing.
		return "", "", fmt.Sprintf("id %q is ambiguous among waiting jobs (%s)", fields[0], waitingIDList(waiting))
	}
}

// handleAnswerCommand resolves and delivers a "/answer" command's argument
// against reg, on behalf of a human — Registry.Answer's fromUser=true is
// what lets the "ask" tool tell the child that this reply is the user's
// word, not another agent's guess (see tools/ask.go's AskTool.Run).
func handleAnswerCommand(reg *jobs.Registry, arg string) (msg string, ok bool) {
	waiting := waitingJobs(reg)
	jobID, text, errMsg := resolveAnswerTarget(waiting, arg)
	if errMsg != "" {
		return errMsg, false
	}
	if !reg.Answer(jobID, text, true) {
		return fmt.Sprintf("job %s is no longer waiting for an answer", jobID), false
	}
	return fmt.Sprintf("answer delivered to job %s", jobID), true
}
