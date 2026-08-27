package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
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
	// ID as a tiebreaker: StartedAt/FinishedAt can collide (same-instant
	// timestamps), and sort.Slice is not stable — without this two jobs
	// finishing at the same wall-clock moment could swap order between two
	// "/jobs" calls. Mirrors display/tui_jobs_panel.go's sortedBackgroundJobs.
	sort.Slice(waiting, func(i, k int) bool {
		if !waiting[i].StartedAt.Equal(waiting[k].StartedAt) {
			return waiting[i].StartedAt.Before(waiting[k].StartedAt)
		}
		return waiting[i].ID < waiting[k].ID
	})
	sort.Slice(running, func(i, k int) bool {
		if !running[i].StartedAt.Equal(running[k].StartedAt) {
			return running[i].StartedAt.Before(running[k].StartedAt)
		}
		return running[i].ID < running[k].ID
	})
	sort.Slice(terminal, func(i, k int) bool {
		if !terminal[i].FinishedAt.Equal(terminal[k].FinishedAt) {
			return terminal[i].FinishedAt.After(terminal[k].FinishedAt)
		}
		return terminal[i].ID < terminal[k].ID
	})

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

// consoleJobLineMaxTextLen caps the description/question text so one job
// never spans more than one printed line even before the collapseToOneLine
// pass — matches formatJobLine's width truncation in spirit, without
// needing this file to know the terminal width the way the TUI panel does.
const consoleJobLineMaxTextLen = 200

// consoleJobLine mirrors formatJobLine's content (display/tui_jobs_panel.go)
// without the lipgloss styling console output doesn't use elsewhere in this
// file: "<status> #<id> <description-or-question> (<duration>)".
func consoleJobLine(j jobs.Job) string {
	text := j.Description
	if j.Status == jobs.StatusWaitingAnswer && j.Question != "" {
		text = fmt.Sprintf("asks: %q", j.Question)
	}
	text = collapseToOneLine(text, consoleJobLineMaxTextLen)
	dur := j.FinishedAt.Sub(j.StartedAt)
	if j.FinishedAt.IsZero() {
		dur = time.Since(j.StartedAt)
	}
	return fmt.Sprintf("%-14s #%s %s (%s)", j.Status, jobs.ShortID(j.ID), text, dur.Round(time.Second))
}

// collapseToOneLine folds any newlines to spaces and truncates with an
// ellipsis past maxLen — a description or (ask_parent-supplied, free-form)
// question can contain either, and printJobs's one-line-per-job contract
// (the overflow count assumes it) must hold regardless.
func collapseToOneLine(s string, maxLen int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return "…"
	}
	return string(runes[:maxLen-1]) + "…"
}
