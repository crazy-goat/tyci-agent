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
//   - "#<id> <text>" — id is the short suffix the jobs panel displays (e.g.
//     "#3"), or the job's full id, prefixed with "#". Always treated as an
//     id, and an error if it matches nothing — never a silent fallback.
//   - "<n> <text>" — a BARE number is only treated as an id when more than
//     one job is waiting, i.e. when there is something to disambiguate.
//     With exactly one job waiting a leading number is just the first word
//     of the answer text: "/answer 5 minutes is fine" must not be parsed
//     as "answer job #5 with 'minutes is fine'" when #5 is the only job
//     around to answer in the first place.
//   - "<text>" alone — allowed only when exactly one job is waiting, the
//     common case; with more than one waiting, an id is required.
//
// Returns either (job, text) or a user-facing errMsg — never both.
func resolveAnswerTarget(waiting []jobs.Job, arg string) (job jobs.Job, text, errMsg string) {
	arg = strings.TrimSpace(arg)
	if len(waiting) == 0 {
		return jobs.Job{}, "", "no job is currently waiting for an answer"
	}
	if arg == "" {
		return jobs.Job{}, "", "usage: /answer [#id] <answer text>"
	}

	fields := strings.Fields(arg)
	head := fields[0]
	hashed := strings.HasPrefix(head, "#")
	candidate := strings.TrimPrefix(head, "#")

	// Only attempt to read the first word as an id at all when it is
	// explicitly marked with "#", or when there is more than one job
	// waiting (see doc comment above for why a lone waiting job changes
	// this: there is nothing to disambiguate, so nothing to parse as an id).
	if hashed || len(waiting) > 1 {
		var matches []jobs.Job
		for _, j := range waiting {
			if candidate == j.ID || candidate == jobs.ShortID(j.ID) {
				matches = append(matches, j)
			}
		}
		switch len(matches) {
		case 1:
			rest := strings.TrimSpace(strings.TrimPrefix(arg, head))
			if rest == "" {
				return jobs.Job{}, "", "usage: /answer [#id] <answer text>"
			}
			return matches[0], rest, ""
		case 0:
			if hashed {
				// An explicit id that matched nothing is a mistake worth
				// stopping for, not a reason to guess against a different
				// job or eat it into the answer text.
				return jobs.Job{}, "", fmt.Sprintf("no job matching id %q is waiting (waiting: %s)", head, waitingIDList(waiting))
			}
			// Not "#"-prefixed and it didn't match anything either: treat
			// it as plain text below, same as if len(waiting) == 1.
		default:
			// Should not happen in practice (short ids are unique for the
			// life of the process), but resolve unambiguously rather than
			// guessing.
			return jobs.Job{}, "", fmt.Sprintf("id %q is ambiguous among waiting jobs (%s)", head, waitingIDList(waiting))
		}
	}

	if len(waiting) == 1 {
		return waiting[0], arg, ""
	}
	return jobs.Job{}, "", fmt.Sprintf("multiple jobs waiting (%s) — specify one: /answer <id> <text>", waitingIDList(waiting))
}

// handleAnswerCommand resolves and delivers a "/answer" command's argument
// against reg, on behalf of a human — Registry.Answer's fromUser=true is
// what lets the "ask" tool tell the child that this reply is the user's
// word, not another agent's guess (see tools/ask.go's AskTool.Run).
//
// The confirmation echoes which job and which question were answered: a
// short id can resolve to the wrong job (see resolveAnswerTarget's doc
// comment on ambiguous bare numbers with several jobs waiting), and a
// silent "delivered" would leave that invisible until the wrong child
// surfaces a confused result minutes later.
func handleAnswerCommand(reg *jobs.Registry, arg string) (msg string, ok bool) {
	waiting := waitingJobs(reg)
	job, text, errMsg := resolveAnswerTarget(waiting, arg)
	if errMsg != "" {
		return errMsg, false
	}
	if !reg.Answer(job.ID, text, true) {
		return fmt.Sprintf("job #%s is no longer waiting for an answer", jobs.ShortID(job.ID)), false
	}
	return fmt.Sprintf("answered job #%s (asked: %q) with: %q", jobs.ShortID(job.ID), job.Question, text), true
}
