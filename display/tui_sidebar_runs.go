package display

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// TuiRunRow is one workflow run in the sidebar Runs tab.
type TuiRunRow struct {
	ID     string
	Issue  int
	Status string // running|paused|done|failed|stopped
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
// line: the index of the selectable row (runsItem) the line belongs to, or -1
// for the separator and the notices. start has one entry per selectable row:
// the index of its first line.
type runsView struct {
	lines []string
	owner []int
	start []int
}

// runsItem is one selectable row of the Runs tab: a run row, or an agent row
// under an expanded run. The cursor is an index into the list of runsItem.
type runsItem struct {
	run   int // index into the buildTree rows
	agent int // index into the agents of that run, or -1 for the run row
}

// runsCursorKey identifies a Runs row: the run id, and the agent job id for an
// agent row. The agent id is empty on a run row.
type runsCursorKey struct {
	run   string
	agent string
}

// runsItemKey returns the key of the item it of the tree row t.
func runsItemKey(t treeRun, it runsItem) runsCursorKey {
	k := runsCursorKey{run: t.run.ID}
	if it.agent >= 0 {
		k.agent = t.agents[it.agent].job.ID
	}
	return k
}

// runsItemIndex returns the index in items of the row with key, or -1.
func runsItemIndex(tree []treeRun, items []runsItem, key runsCursorKey) int {
	for i, it := range items {
		if runsItemKey(tree[it.run], it) == key {
			return i
		}
	}
	return -1
}

// sidebarRememberRun stores the Runs row under the cursor in sidebarRunKey. It
// must run after each move of the Runs cursor, so that sidebarRunsResolve finds
// the same row again.
func (m *TuiModel) sidebarRememberRun() {
	if m.sidebarTab != sidebarTabRuns {
		return
	}
	// The index is read raw: the cursor was just written, and the old key
	// must not override it.
	tree := m.buildTree()
	items := m.runsItems(tree)
	if m.sidebarCursor < 0 || m.sidebarCursor >= len(items) {
		m.sidebarRunKey = runsCursorKey{}
		return
	}
	m.sidebarRunKey = runsItemKey(tree[items[m.sidebarCursor].run], items[m.sidebarCursor])
}

// sidebarRunsResolve sets the cursor to the row of sidebarRunKey and stores
// that row as the key. It runs before each Runs key action and on each tick, so
// a key acts on the row the user sees, as sidebarBusResolve does on the Bus tab.
// It keeps the row inside the visible window. It does nothing when the sidebar
// is closed or another tab is open.
func (m *TuiModel) sidebarRunsResolve() {
	if !m.sidebarActive || m.sidebarTab != sidebarTabRuns {
		return
	}
	tree := m.buildTree()
	items := m.runsItems(tree)
	if len(items) == 0 {
		m.sidebarCursor = 0
		m.sidebarRunKey = runsCursorKey{}
		return
	}
	m.sidebarCursor = min(max(m.runsCursor(tree, items), 0), len(items)-1)
	m.sidebarRunKey = runsItemKey(tree[items[m.sidebarCursor].run], items[m.sidebarCursor])
	m.sidebarClampScrollToCursor(m.sidebarLayout().contentHeight)
}

func (v *runsView) add(line string, run int) {
	v.lines = append(v.lines, line)
	v.owner = append(v.owner, run)
}

// runFinished reports whether a run is over. Every other run is active.
func runFinished(r TuiRunRow) bool {
	return r.Status == "done" || r.Status == "failed" || r.Status == "stopped"
}

// runsItems returns the selectable rows of the Runs tab in display order: each
// run row, and the agent rows of the expanded runs. The cursor, the drawing,
// Enter and the stop key all use this list only.
func (m TuiModel) runsItems(tree []treeRun) []runsItem {
	var items []runsItem
	for i, t := range tree {
		items = append(items, runsItem{run: i, agent: -1})
		if !m.sidebarRunsExpanded[t.run.ID] {
			continue
		}
		for j := range t.agents {
			items = append(items, runsItem{run: i, agent: j})
		}
	}
	return items
}

// runsCursorItem returns the tree row and the item under the cursor. ok is
// false when the cursor is on no row.
func (m TuiModel) runsCursorItem() (t treeRun, it runsItem, ok bool) {
	tree := m.buildTree()
	items := m.runsItems(tree)
	i := m.runsCursor(tree, items)
	if i < 0 || i >= len(items) {
		return treeRun{}, runsItem{}, false
	}
	it = items[i]
	return tree[it.run], it, true
}

// runsCursor returns the index in items of the row under the Runs cursor. It
// finds the row by sidebarRunKey, so a run that moved keeps its row, the same
// way busCursor works on the Bus tab. Without a key the index is the cursor.
// When the key is gone, the index is clamped to the last row.
func (m TuiModel) runsCursor(tree []treeRun, items []runsItem) int {
	if m.sidebarRunKey == (runsCursorKey{}) {
		return m.sidebarCursor
	}
	if i := runsItemIndex(tree, items, m.sidebarRunKey); i >= 0 {
		return i
	}
	if i := runsItemIndex(tree, items, runsCursorKey{run: m.sidebarRunKey.run}); i >= 0 {
		return i
	}
	return min(max(m.sidebarCursor, 0), len(items)-1)
}

func (m TuiModel) renderSidebarRuns(width int) []string {
	return m.runsTab(width).lines
}

// runsTab lays the Runs tab out at width. A run is one line, or several when
// it is expanded (Enter or a click). The separator sits between the active
// and the finished runs.
func (m TuiModel) runsTab(width int) runsView {
	var v runsView
	tree := m.buildTree()
	if len(tree) == 0 {
		v.add("", -1)
		if m.runLister == nil {
			v.add("  Workflow runs are not wired up in this build.", -1)
		} else {
			v.add("  No workflow runs yet.", -1)
		}
		return v
	}
	items := m.runsItems(tree)
	cursor := m.runsCursor(tree, items)
	rows := make([]TuiRunRow, 0, len(tree))
	for _, t := range tree {
		rows = append(rows, t.run)
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	now := time.Now()
	durW, costW := runsColumnWidths(rows, now)
	for i, t := range tree {
		r := t.run
		if i > 0 && runFinished(r) && !runFinished(rows[i-1]) {
			v.add(dim.Render(strings.Repeat("─", width)), -1)
		}
		item := len(v.start)
		v.start = append(v.start, len(v.lines))
		selected := item == cursor
		style := rowStyle(selected)
		if !selected && runFinished(r) {
			style = dim
		}
		v.add(style.Render(runRowLine(" "+runHeader(r), runDuration(r, now), r.Cost, width, durW, costW)), item)
		if !m.sidebarRunsExpanded[r.ID] {
			continue
		}
		v.add(dim.Render(fillWidth("   "+r.ID, width)), item)
		for _, s := range r.Steps {
			v.add(dim.Render(lineWithRight("   "+s.Text, s.Cost, width)), item)
		}
		if !runFinished(r) && !r.Since.IsZero() {
			v.add(fillWidth("   "+runStepText(r, now), width), item)
		}
		for j, a := range t.agents {
			ai := item + 1 + j
			v.start = append(v.start, len(v.lines))
			v.add(agentRowLine(a, width, ai == cursor), ai)
		}
	}
	if m.sidebarStopRun != "" {
		v.add(dim.Render(fillWidth(" Stop run "+m.sidebarStopRun+"? press y to confirm", width)), -1)
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

// runHeader is the label of a run without its time and cost: "#3 issue #472
// done (merged)" for a finished run, "#4 issue #632 code (worker)" for an active
// one. The run id comes first. The ad-hoc row is labelled "ad-hoc".
func runHeader(r TuiRunRow) string {
	if r.ID == treeAdHocID {
		return treeAdHocID
	}
	// The run id and the issue are both shown: a bare number is ambiguous, as a
	// run number and an issue number look the same.
	head := r.ID
	if r.Issue > 0 {
		head += fmt.Sprintf(" issue #%d", r.Issue)
	}
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
// or "needs a human: code" when the run is paused. A check step has no role: "ci_wait (script)".
func runStep(r TuiRunRow) string {
	switch {
	case r.Status == "paused":
		return "needs a human: " + r.State
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
// cut first when both do not fit. right is dropped when no gap is left. The
// line is always exactly width columns wide, see fillWidth.
func lineWithRight(left, right string, width int) string {
	rw := cellWidth(right)
	if right == "" || width <= rw+1 {
		return fillWidth(left, width)
	}
	left = truncateToWidth(left, width-rw-1)
	return left + strings.Repeat(" ", width-cellWidth(left)-rw) + right
}

// fillWidth cuts s to width columns and pads it with spaces to exactly width.
// A row must be padded before its style renders it: the reset at the end of a
// styled string clears the sidebar background, so padding added after the
// reset is drawn without that background.
func fillWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := cellWidth(s)
	if w > width {
		s = truncateToWidth(s, width)
		w = cellWidth(s)
	}
	return s + strings.Repeat(" ", max(0, width-w))
}

// fitCells joins the right-hand cells of a row with gap spaces. When the row is
// too narrow to leave a column for the left part, the first cell is dropped,
// then the next, until the rest fits. It returns "" when no cell fits.
func fitCells(cells []string, gap, width int) string {
	for len(cells) > 0 {
		right := strings.Join(cells, strings.Repeat(" ", gap))
		if width > cellWidth(right)+1 {
			return right
		}
		cells = cells[1:]
	}
	return ""
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
	return lineWithRight(left, fitCells(cells, runColumnGap, width), width)
}

// padLeft right-aligns s in width columns.
func padLeft(s string, width int) string {
	return strings.Repeat(" ", max(0, width-cellWidth(s))) + s
}
