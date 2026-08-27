package main

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/decodo/tyci/jobs"
)

// maxConsoleTerminalJobLines caps how many finished jobs "/jobs" prints, the
// console equivalent of display/tui_jobs_panel.go's jobsPanelMaxLines. A
// running or waiting_answer job is never capped — those are exactly the
// ones a person typing "/jobs" wants to see, especially a blocked question,
// which is invisible everywhere else in console mode (item 36): the TUI has
// an always-on panel and a distinct icon for it; the console has neither,
// so a person has had no way to notice a child waiting for input short of
// the model choosing to relay it in its own reply.
const maxConsoleTerminalJobLines = 10

// printJobs renders every job in the registry as one line each, live jobs
// (waiting_answer, then running) first and always shown in full, followed by
// up to maxConsoleTerminalJobLines of the most recently finished ones. A
// waiting question shows the question text instead of the description —
// same convention as formatJobLine (display/tui_jobs_panel.go) — since
// that's the one thing worth reading at a glance.
func printJobs(w io.Writer, all []jobs.Job) {
	if len(all) == 0 {
		fmt.Fprintln(w, "No background jobs.")
		return
	}

	var waiting, running, terminal []jobs.Job
	for _, j := range all {
		switch j.Status {
		case jobs.StatusWaitingAnswer:
			waiting = append(waiting, j)
		case jobs.StatusRunning:
			running = append(running, j)
		default:
			terminal = append(terminal, j)
		}
	}
	sort.Slice(waiting, func(i, k int) bool { return waiting[i].StartedAt.Before(waiting[k].StartedAt) })
	sort.Slice(running, func(i, k int) bool { return running[i].StartedAt.Before(running[k].StartedAt) })
	sort.Slice(terminal, func(i, k int) bool { return terminal[i].FinishedAt.After(terminal[k].FinishedAt) })

	for _, j := range waiting {
		fmt.Fprintln(w, consoleJobLine(j))
	}
	for _, j := range running {
		fmt.Fprintln(w, consoleJobLine(j))
	}
	shown := terminal
	hidden := 0
	if len(shown) > maxConsoleTerminalJobLines {
		hidden = len(shown) - maxConsoleTerminalJobLines
		shown = shown[:maxConsoleTerminalJobLines]
	}
	for _, j := range shown {
		fmt.Fprintln(w, consoleJobLine(j))
	}
	if hidden > 0 {
		fmt.Fprintf(w, "… and %d more finished job(s)\n", hidden)
	}
}

// consoleJobLine mirrors formatJobLine's content (display/tui_jobs_panel.go)
// without the lipgloss styling console output doesn't use elsewhere in this
// file: "<status> #<id> <description-or-question> (<duration>)".
func consoleJobLine(j jobs.Job) string {
	text := j.Description
	if j.Status == jobs.StatusWaitingAnswer && j.Question != "" {
		text = fmt.Sprintf("asks: %q", j.Question)
	}
	dur := j.FinishedAt.Sub(j.StartedAt)
	if j.FinishedAt.IsZero() {
		dur = time.Since(j.StartedAt)
	}
	return fmt.Sprintf("%-14s #%s %s (%s)", j.Status, jobs.ShortID(j.ID), text, dur.Round(time.Second))
}
