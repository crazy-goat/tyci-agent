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
	durW, costW := runsColumnWidths(rows, now)
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
		v.add(style.Render(runRowLine(" "+runHeader(r), runDuration(r, now), r.Cost, width, durW, costW)), i)
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

// runColumnGap is the number of spaces between the duration and the cost of a run row.
const runColumnGap = 3

// runsColumnWidths returns the widths of the duration column and the cost column
// of the run rows. Every row uses the same widths, so the columns line up.
func runsColumnWidths(rows []TuiRunRow, now time.Time) (durW, costW int) {
	for _, r := range rows {
		durW = max(durW, lipgloss.Width(runDuration(r, now)))
		costW = max(costW, lipgloss.Width(r.Cost))
	}
	return durW, costW
}

// runHeader is the label of a run without its time and cost: "#472 done
// (merged)" for a finished run, "#632 code (worker)" for an active one.
func runHeader(r TuiRunRow) string {
	head := fmt.Sprintf("#%d", r.Issue)
	if !runFinished(r) {
		return head + " " + runStep(r)
	}
	head += " " + r.Status
	if r.Result != "" {
		head += " (" + r.Result + ")"
	}
	return head
}

// runDuration is the time column of a run row: how long the current step, or
// the pause, of an active run runs so far, or the time of the finished steps of
// a finished run. Empty when there is no time to show.
func runDuration(r TuiRunRow, now time.Time) string {
	if runFinished(r) {
		if r.Took > 0 {
			return fmtRunDuration(r.Took)
		}
		return ""
	}
	if r.Since.IsZero() {
		return ""
	}
	return fmtRunDuration(now.Sub(r.Since))
}

// runStep is the current step of an active run without its time: "code (worker)",
// or "ask: code" when the run is paused. A check step has no role: "ci_wait (script)".
func runStep(r TuiRunRow) string {
	switch {
	case r.Status == "paused":
		return "ask: " + r.State
	case r.Role != "":
		return r.State + " (" + r.Role + ")"
	default:
		return r.State + " (script)"
	}
}

// runStepText is the current step of an active run with its time: "code (worker) 19s".
func runStepText(r TuiRunRow, now time.Time) string {
	if dur := runDuration(r, now); dur != "" {
		return runStep(r) + " " + dur
	}
	return runStep(r)
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

// runRowLine lays out a run row: left, then the duration and the cost, both
// right-aligned in columns durW and costW wide. When the row is too narrow, left
// is cut first, then the duration column is dropped, then the cost column.
func runRowLine(left, dur, cost string, width, durW, costW int) string {
	var cells []string
	if durW > 0 {
		cells = append(cells, padLeft(dur, durW))
	}
	if costW > 0 {
		cells = append(cells, padLeft(cost, costW))
	}
	for len(cells) > 0 {
		right := strings.Join(cells, strings.Repeat(" ", runColumnGap))
		if width > lipgloss.Width(right)+1 {
			return lineWithRight(left, right, width)
		}
		cells = cells[1:]
	}
	return truncateToWidth(left, width)
}

// padLeft right-aligns s in width columns.
func padLeft(s string, width int) string {
	return strings.Repeat(" ", max(0, width-lipgloss.Width(s))) + s
}
