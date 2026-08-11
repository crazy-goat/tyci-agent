package display

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/jobs"
)

// jobsPanelMaxLines caps the inline panel the same way renderQueuePanel caps
// the pending-message panel: beyond this many rows, an "… and N more" line
// takes the place of the rest.
const jobsPanelMaxLines = 4

// applyJobUpdate upserts j into backgroundJobs. Called from Update() for
// every tuiMsgJobUpdate, regardless of which overlay (if any) is active.
func (m *TuiModel) applyJobUpdate(j jobs.Job) {
	if m.backgroundJobs == nil {
		m.backgroundJobs = make(map[string]jobs.Job)
	}
	m.backgroundJobs[j.ID] = j
	// The panel's height depends on len(backgroundJobs), which changes the
	// message viewport height (see renderFrame's jobsH). invalidateTotalLines
	// also marks the cached message region dirty so a job starting/finishing
	// while the region is otherwise unchanged (same scroll/width/selection)
	// doesn't render with a stale height — see buildMessageRegionCached.
	m.invalidateTotalLines()
}

// sortedBackgroundJobs returns backgroundJobs as a slice, newest-first
// (by StartedAt), with ID as a stable tiebreaker so render output doesn't
// jitter between frames for jobs started in the same instant.
func (m TuiModel) sortedBackgroundJobs() []jobs.Job {
	if len(m.backgroundJobs) == 0 {
		return nil
	}
	out := make([]jobs.Job, 0, len(m.backgroundJobs))
	for _, j := range m.backgroundJobs {
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool {
		if !out[i].StartedAt.Equal(out[k].StartedAt) {
			return out[i].StartedAt.After(out[k].StartedAt)
		}
		return out[i].ID > out[k].ID
	})
	return out
}

// jobStatusIcon returns a short glyph for a job's status, mirroring the
// icon/color scheme formatTodoModalLine uses for todo items.
func jobStatusIcon(status jobs.Status) (icon string, color lipgloss.TerminalColor) {
	switch status {
	case jobs.StatusRunning:
		return "⟳", lipgloss.Color("214") // orange
	case jobs.StatusDone:
		return "✓", lipgloss.Color("114") // green
	case jobs.StatusFailed:
		return "✗", lipgloss.Color("203") // red
	case jobs.StatusTruncated:
		return "⚠", lipgloss.Color("214") // orange
	default:
		return "?", lipgloss.Color("245")
	}
}

// jobDuration returns how long j has been (or was) running: FinishedAt-
// StartedAt once finished, otherwise now-StartedAt for a live counter.
func jobDuration(j jobs.Job) time.Duration {
	if j.Status == jobs.StatusRunning {
		return time.Since(j.StartedAt).Round(time.Second)
	}
	return j.FinishedAt.Sub(j.StartedAt).Round(time.Second)
}

// shortJobID trims the "job-<unixnano>-<n>" ID (see jobs.nextID) down to a
// stable, human-scannable suffix instead of showing the full timestamp.
func shortJobID(id string) string {
	if idx := strings.LastIndexByte(id, '-'); idx >= 0 && idx+1 < len(id) {
		return id[idx+1:]
	}
	return id
}

// formatJobLine renders one job as a single line: "<icon> #<id> <status>
// <description> (<duration>)", truncated to fit width.
func formatJobLine(j jobs.Job, width int) string {
	icon, color := jobStatusIcon(j.Status)
	iconStyled := lipgloss.NewStyle().Foreground(color).Render(icon)
	prefix := fmt.Sprintf("%s #%s %-9s ", iconStyled, shortJobID(j.ID), j.Status)
	suffix := fmt.Sprintf(" (%s)", jobDuration(j))
	// Reserve room for prefix/suffix (measured without ANSI codes via
	// lipgloss.Width, which strips styling) before truncating the
	// description into what's left.
	avail := width - lipgloss.Width(prefix) - lipgloss.Width(suffix)
	if avail < 1 {
		avail = 1
	}
	desc := truncateString(j.Description, avail)
	return truncateToWidth(prefix+desc+suffix, width)
}

// renderJobsPanel renders the inline background-jobs panel that appears
// between the status bar and the queue panel. Returns "" when there are no
// background jobs, so a user who never uses subagent(async: true) sees no
// layout change at all (mirrors renderQueuePanel's empty-queue contract).
func (m TuiModel) renderJobsPanel(width int) string {
	list := m.sortedBackgroundJobs()
	if len(list) == 0 {
		return ""
	}
	if width < 1 {
		width = 1
	}

	style := lipgloss.NewStyle().
		Width(width).MaxWidth(width).
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("250"))

	var b strings.Builder
	n := len(list)
	if n > jobsPanelMaxLines {
		n = jobsPanelMaxLines
	}
	for i := 0; i < n; i++ {
		b.WriteString(style.Render(formatJobLine(list[i], width)))
		b.WriteString("\n")
	}
	if len(list) > jobsPanelMaxLines {
		more := len(list) - jobsPanelMaxLines
		moreLine := truncateToWidth(fmt.Sprintf("… and %d more (Ctrl+B for full list)", more), width)
		b.WriteString(style.Render(moreLine))
		b.WriteString("\n")
	}
	return b.String()
}

// jobsPanelHeight returns the terminal rows renderJobsPanel occupies at the
// current backgroundJobs size, mirroring queuePanelHeight.
func (m TuiModel) jobsPanelHeight() int {
	n := len(m.backgroundJobs)
	if n == 0 {
		return 0
	}
	if n > jobsPanelMaxLines {
		return jobsPanelMaxLines + 1
	}
	return n
}
