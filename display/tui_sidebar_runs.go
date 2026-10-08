package display

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// TuiRunRow is one workflow run in the sidebar Runs tab.
type TuiRunRow struct {
	ID     string
	Issue  int
	Status string // running|paused|done|failed
	State  string // current state
	Role   string // role of the running agent, empty for a check state
	Result string // outcome of a finished run, "merged" or empty
	// Started is the start of the run, Ended the end of a finished run.
	Started time.Time
	Ended   time.Time
	// Since is the start of the current step, or of the pause. Zero for a
	// finished run.
	Since time.Time
	// Took is the time of the finished steps: the run time without the pauses.
	Took time.Duration
	Cost string // total cost of the run, "$0.52"
	// Steps has every finished visit of the run, oldest first.
	Steps []TuiRunStep
}

// TuiRunStep is one finished visit of a run, one line in the expanded run.
type TuiRunStep struct {
	Text string // "code -> ok: worker · model · 1m52s"
	Cost string // "$0.01"; empty for a check step
}

// runsView is the Runs tab laid out at one width. owner has one entry per
// line: the index of the run the line belongs to, or -1 for the separator and
// the notices. start has one entry per run: the index of its first line.
type runsView struct {
	lines []string
	owner []int
	start []int
}

func (v *runsView) add(line string, run int) {
	v.lines = append(v.lines, line)
	v.owner = append(v.owner, run)
}

// runFinished reports whether a run is over. Every other run is active.
func runFinished(r TuiRunRow) bool {
	return r.Status == "done" || r.Status == "failed"
}

// sidebarRunRows returns the runs in display order: the active runs by start,
// newest first, then the finished runs by end, newest first. The index of a
// row is the cursor position of its run.
func (m TuiModel) sidebarRunRows() []TuiRunRow {
	if m.runLister == nil {
		return nil
	}
	var active, finished []TuiRunRow
	for _, r := range m.runLister() {
		if runFinished(r) {
			finished = append(finished, r)
		} else {
			active = append(active, r)
		}
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].Started.After(active[j].Started) })
	sort.SliceStable(finished, func(i, j int) bool { return finished[i].Ended.After(finished[j].Ended) })
	return append(active, finished...)
}

func (m TuiModel) renderSidebarRuns(width int) []string {
	return m.runsTab(width).lines
}

// runsTab lays the Runs tab out at width. A run is one line, or several when
// it is expanded (Enter or a click). The separator sits between the active
// and the finished runs.
func (m TuiModel) runsTab(width int) runsView {
	var v runsView
	if m.runLister == nil {
		v.add("", -1)
		v.add("  Workflow runs are not wired up in this build.", -1)
		return v
	}
	rows := m.sidebarRunRows()
	if len(rows) == 0 {
		v.add("", -1)
		v.add("  No workflow runs yet.", -1)
		return v
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	now := time.Now()
	for i, r := range rows {
		if i > 0 && runFinished(r) && !runFinished(rows[i-1]) {
			v.add(dim.Render(strings.Repeat("─", width)), -1)
		}
		v.start = append(v.start, len(v.lines))
		selected := i == m.sidebarCursor
		style := rowStyle(width, selected)
		if !selected && runFinished(r) {
			style = dim
		}
		v.add(style.Render(lineWithRight(" "+runHeader(r, now), r.Cost, width)), i)
		if !m.sidebarRunsExpanded[r.ID] {
			continue
		}
		v.add(dim.Render(truncateToWidth("   "+r.ID, width)), i)
		for _, s := range r.Steps {
			v.add(dim.Render(lineWithRight("   "+s.Text, s.Cost, width)), i)
		}
		if !runFinished(r) && !r.Since.IsZero() {
			v.add(truncateToWidth("   "+runStepText(r, now), width), i)
		}
	}
	return v
}

// runHeader is the one-line view of a run without its cost: "#472 done
// (merged) 12m3s" for a finished run, "#632 code (worker) 19s" for an active one.
func runHeader(r TuiRunRow, now time.Time) string {
	head := fmt.Sprintf("#%d", r.Issue)
	if !runFinished(r) {
		return head + " " + runStepText(r, now)
	}
	head += " " + r.Status
	if r.Result != "" {
		head += " (" + r.Result + ")"
	}
	if r.Took > 0 {
		head += " " + fmtRunDuration(r.Took)
	}
	return head
}

// runStepText is the current step of an active run: "code (worker) 19s", or
// "ask: code 19s" when the run is paused. A check step has no role: "script".
// The time is how long the step runs so far.
func runStepText(r TuiRunRow, now time.Time) string {
	text := r.State
	switch {
	case r.Status == "paused":
		text = "ask: " + text
	case r.Role != "":
		text += " (" + r.Role + ")"
	default:
		text += " (script)"
	}
	if !r.Since.IsZero() {
		text += " " + fmtRunDuration(now.Sub(r.Since))
	}
	return text
}

// fmtRunDuration rounds a duration to the second: "19s", "1m52s".
func fmtRunDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

// lineWithRight puts right at the end of a line width columns wide. left is
// cut first when both do not fit. right is dropped when no gap is left.
func lineWithRight(left, right string, width int) string {
	rw := lipgloss.Width(right)
	if right == "" || width <= rw+1 {
		return truncateToWidth(left, width)
	}
	left = truncateToWidth(left, width-rw-1)
	return left + strings.Repeat(" ", width-lipgloss.Width(left)-rw) + right
}
