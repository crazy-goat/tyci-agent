package display

// The right-side sidebar (TODO.md item 1): Tokens / Sessions / Bash / Lua /
// Subagents tabs, toggled with Ctrl+T or by clicking the status bar's
// context figure (see tui_status.go's statusRightHit).
//
// It is rendered as a full-screen overlay — the same family as the jobs
// modal, todo modal, /btw modal and resume picker already in this file set
// — rather than a live side-by-side column that narrows the transcript.
// TODO.md's own writeup flags exactly why a live column is the riskier
// choice: tui_view.go builds full-width rows, the render caches are
// width-keyed (item 30 documents a cache bug from exactly this kind of
// mismatch), and mouse hit-testing would need to clamp to a narrower main
// column throughout tui_mouse.go. An overlay reuses all of that
// infrastructure completely unchanged and carries none of that risk, at the
// cost of not being visible at the same time as the transcript — the same
// trade-off every other popup in this file already makes. It renders
// right-anchored (lipgloss.Right) so it still reads visually as a sidebar,
// not a centered dialog.
//
// Data sources, deliberately reused rather than duplicated:
//   - Tokens: buildUsageDetail (tui_tokens.go), previously dead code (Inbox
//     item F11) — this is its first production caller.
//   - Sessions: session.ResumeEntries via TUI.SetSessionLister, the same
//     source bare "/resume" already uses. Re-entering a session goes through
//     the exact same path a typed "/resume" would (see sidebarSubmitResume):
//     no second resume mechanism.
//   - Bash / Subagents: jobs.Job rows already mirrored into
//     m.backgroundJobs by TUI.SetJobEventBus, filtered on the Kind field
//     item 1 added to jobs.Job.
//   - Lua: tools.LuaRunHistory, a small new process-local ring buffer (Lua
//     tools run synchronously to completion, so there is no "still running"
//     state to track the way bash's background handoff has).
//   - Subagents' per-child tokens/cost: internal/ledger.UsageByJob plus a
//     ParentID walk — see buildSubagentTree below.

import (
	"fmt"
	"sort"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

const (
	sidebarTabTokens = iota
	sidebarTabSessions
	sidebarTabBash
	sidebarTabLua
	sidebarTabSubagents
	sidebarTabCount
)

var sidebarTabNames = [sidebarTabCount]string{
	sidebarTabTokens:    "Tokens",
	sidebarTabSessions:  "Sessions",
	sidebarTabBash:      "Bash",
	sidebarTabLua:       "Lua",
	sidebarTabSubagents: "Subagents",
}

// openSidebar opens the sidebar on the given tab, saving scroll state the
// same way every other full-screen overlay in this package does.
func (m *TuiModel) openSidebar(tab int) {
	if tab < 0 || tab >= sidebarTabCount {
		tab = sidebarTabTokens
	}
	if !m.sidebarActive {
		m.savedScrollLine = m.scrollLine
		m.savedAtBottom = m.atBottom
	}
	m.sidebarActive = true
	m.sidebarTab = tab
	m.sidebarCursor = 0
}

// closeSidebar closes the sidebar and restores scroll state.
func (m *TuiModel) closeSidebar() {
	m.sidebarActive = false
	m.sidebarCursor = 0
	m.atBottom = m.savedAtBottom
	m.scrollLine = m.savedScrollLine
	m.selectionVersion++
	m.selection = SelectionState{}
	m.selectionFlash = false
}

// toggleSidebar is Ctrl+T's handler: close if open, otherwise reopen on
// whichever tab was last selected (Tokens, the zero value, the first time).
func (m *TuiModel) toggleSidebar() {
	if m.sidebarActive {
		m.closeSidebar()
		return
	}
	m.openSidebar(m.sidebarTab)
}

// sidebarRowCount returns how many selectable rows the current tab has, for
// clamping sidebarCursor on Up/Down/wheel.
func (m TuiModel) sidebarRowCount() int {
	switch m.sidebarTab {
	case sidebarTabSessions:
		return len(m.sidebarSessionEntries())
	case sidebarTabBash:
		return len(m.sidebarBashJobs())
	case sidebarTabLua:
		return len(tools.LuaRunHistory())
	case sidebarTabSubagents:
		return len(m.buildSubagentTree())
	default:
		return 0
	}
}

// ─── Update handler ─────────────────────────────────────────────────────────

func (m TuiModel) updateSidebar(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape:
			m.closeSidebar()
			return m, nil

		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit

		case tea.KeyCtrlT:
			m.closeSidebar()
			return m, nil

		case tea.KeyTab:
			m.sidebarTab = (m.sidebarTab + 1) % sidebarTabCount
			m.sidebarCursor = 0
			return m, nil

		case tea.KeyShiftTab:
			m.sidebarTab = (m.sidebarTab - 1 + sidebarTabCount) % sidebarTabCount
			m.sidebarCursor = 0
			return m, nil

		case tea.KeyLeft:
			m.sidebarTab = (m.sidebarTab - 1 + sidebarTabCount) % sidebarTabCount
			m.sidebarCursor = 0
			return m, nil

		case tea.KeyRight:
			m.sidebarTab = (m.sidebarTab + 1) % sidebarTabCount
			m.sidebarCursor = 0
			return m, nil

		case tea.KeyUp, tea.KeyCtrlUp:
			if m.sidebarCursor > 0 {
				m.sidebarCursor--
			}
			return m, nil

		case tea.KeyDown, tea.KeyCtrlDown:
			if last := m.sidebarRowCount() - 1; m.sidebarCursor < last {
				m.sidebarCursor++
			}
			return m, nil

		case tea.KeyEnter:
			return m.sidebarActivateRow()

		case tea.KeyRunes:
			if m.sidebarTab == sidebarTabSubagents && string(msg.Runes) == "r" {
				return m.sidebarResumeSubagentRow()
			}
			return m, nil
		}

	case tea.MouseMsg:
		layout := m.sidebarLayout()
		if msg.Button == tea.MouseButtonWheelUp {
			if m.sidebarCursor > 0 {
				m.sidebarCursor--
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			if last := m.sidebarRowCount() - 1; m.sidebarCursor < last {
				m.sidebarCursor++
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			inPanel := msg.X >= layout.left && msg.X < layout.left+layout.width &&
				msg.Y >= layout.top && msg.Y < layout.top+layout.height
			if !inPanel {
				m.closeSidebar()
				return m, nil
			}
			// Tab row click.
			if msg.Y == layout.top+1 {
				if tab := sidebarTabAtX(msg.X-layout.left, layout.width); tab >= 0 {
					m.sidebarTab = tab
					m.sidebarCursor = 0
				}
				return m, nil
			}
			// Row click within content area.
			if msg.Y >= layout.contentTop && msg.Y < layout.contentTop+layout.contentHeight {
				row := msg.Y - layout.contentTop
				if row < m.sidebarRowCount() {
					m.sidebarCursor = row
					return m.sidebarActivateRow()
				}
			}
		}
		return m, nil

	case statusMessageClearMsg:
		if m.statusMessage == msg.message {
			m.statusMessage = ""
		}
		return m, nil

	case selectionFlashDoneMsg:
		m.selectionFlash = false
		return m, nil
	}

	return m, nil
}

// sidebarTabAtX maps a click's X (relative to the panel's left edge) to a
// tab index, given the tab row was rendered by joining sidebarTabNames with
// even spacing across width. Returns -1 when x falls outside every tab
// (padding between/around them).
func sidebarTabAtX(x, width int) int {
	if width <= 0 {
		return -1
	}
	cell := width / sidebarTabCount
	if cell <= 0 {
		return -1
	}
	idx := x / cell
	if idx < 0 || idx >= sidebarTabCount {
		return -1
	}
	return idx
}

// sidebarActivateRow is Enter's (and a row click's) handler: what "open
// this row" means depends on the active tab.
func (m TuiModel) sidebarActivateRow() (tea.Model, tea.Cmd) {
	switch m.sidebarTab {
	case sidebarTabSessions:
		return m.sidebarSubmitResume()
	case sidebarTabBash:
		jobsList := m.sidebarBashJobs()
		if m.sidebarCursor >= 0 && m.sidebarCursor < len(jobsList) {
			j := jobsList[m.sidebarCursor]
			m.closeSidebar()
			m.openJobResultModal(j)
		}
		return m, nil
	case sidebarTabSubagents:
		rows := m.buildSubagentTree()
		if m.sidebarCursor >= 0 && m.sidebarCursor < len(rows) {
			row := rows[m.sidebarCursor]
			if !row.isRoot {
				m.closeSidebar()
				m.openJobResultModal(row.job)
			}
		}
		return m, nil
	default:
		return m, nil
	}
}

// sidebarSubmitResume re-enters the Sessions tab's selected session exactly
// the way a person typing bare "/resume" and picking an entry would: it
// submits the literal "/resume" command through the normal input pipeline
// (tui_mode.go's read loop already special-cases it) rather than opening a
// second, sidebar-local resume mechanism. The actual picker — with the same
// entries this tab listed — takes over from there.
func (m TuiModel) sidebarSubmitResume() (tea.Model, tea.Cmd) {
	m.closeSidebar()
	m.input.SetValue("/resume")
	return m.submit(), nil
}

// sidebarResumeSubagentRow is 'r' on a Subagents row: it drafts (but does
// not send) a prompt asking the model to use the existing "resume" tool
// (tools/resume.go) on that job, and closes the sidebar so the user can
// finish the follow-up and press Enter themselves. This is deliberately not
// a second resume mechanism — resume is a model tool, so the honest "UI
// action" here is handing the model a reason to call it, not calling it
// directly. A no-op for the root row or a row whose job cannot be resumed
// (see tools.JobResumer's doc comment); the resume tool call itself will
// report a real reason back to the model if the job turns out not to be
// resumable (e.g. it never produced a usable transcript).
func (m TuiModel) sidebarResumeSubagentRow() (tea.Model, tea.Cmd) {
	rows := m.buildSubagentTree()
	if m.sidebarCursor < 0 || m.sidebarCursor >= len(rows) {
		return m, nil
	}
	row := rows[m.sidebarCursor]
	if row.isRoot {
		return m, nil
	}
	m.closeSidebar()
	m.input.SetValue(fmt.Sprintf("Use the resume tool to continue job %s (%s): ",
		jobs.ShortID(row.job.ID), truncateString(row.job.Description, 40)))
	return m, nil
}

// ─── Data sources ───────────────────────────────────────────────────────────

// sidebarSessionEntries returns this project's resumable sessions,
// newest-first, or nil if no lister was ever wired (see TUI.SetSessionLister)
// or it returned nothing.
func (m TuiModel) sidebarSessionEntries() []TuiResumeEntry {
	if m.sessionLister == nil {
		return nil
	}
	entries := m.sessionLister()
	sorted := make([]TuiResumeEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].ModTime.After(sorted[j].ModTime)
	})
	return sorted
}

// sidebarBashJobs returns backgrounded bash jobs (jobs.KindBash), newest
// first, for the Bash tab.
func (m TuiModel) sidebarBashJobs() []jobs.Job {
	var out []jobs.Job
	for _, j := range m.sortedBackgroundJobs() {
		if j.Kind == jobs.KindBash {
			out = append(out, j)
		}
	}
	return out
}

// subagentTreeRow is one line of the Subagents tab's tree — either the
// synthetic root ("main", the top-level conversation, which is not itself a
// job) or a real job.Job at some depth.
type subagentTreeRow struct {
	depth  int
	isRoot bool
	job    jobs.Job
	// ownTokens is this row's own usage only — tokens deliberately do not
	// roll up (see the package doc comment on why).
	ownTokens int
	// rollupUSD is this row's own cost plus every descendant's, so root's
	// figure agrees with the status bar's total.
	rollupUSD float64
	// rollupUnpriced is true when this row or any descendant used a model
	// with no catalog price — rendered with the status bar's "+?" convention
	// (formatCost) rather than silently understating the bill.
	rollupUnpriced bool
}

// buildSubagentTree walks jobs.Job.ParentID to build the Subagents tab's
// tree, generically to whatever depth the registry actually has (today: one
// level, since a child cannot itself spawn a subagent — see
// subagentDeniedTools — but nothing here assumes that depth). Waiting-answer
// children sort to the top of their sibling group, undimmed at render time;
// everything else keeps sortedBackgroundJobs' newest-first order.
func (m TuiModel) buildSubagentTree() []subagentTreeRow {
	byParent := map[string][]jobs.Job{}
	for _, j := range m.sortedBackgroundJobs() {
		if j.Kind != jobs.KindSubagent {
			continue
		}
		byParent[j.ParentID] = append(byParent[j.ParentID], j)
	}
	for parent, kids := range byParent {
		byParent[parent] = sortSubagentSiblings(kids)
	}

	usage := ledger.UsageByJob()
	snap := ledger.Get()

	rows := []subagentTreeRow{{
		isRoot:         true,
		ownTokens:      snap.Main.Input + snap.Main.Output,
		rollupUSD:      snap.TotalUSD(),
		rollupUnpriced: snap.Unpriced > 0,
	}}

	var walk func(parentID string, depth int)
	walk = func(parentID string, depth int) {
		for _, j := range byParent[parentID] {
			own := usage[j.ID]
			cost, unpriced := rollupJobCost(j.ID, byParent, usage)
			rows = append(rows, subagentTreeRow{
				depth:          depth,
				job:            j,
				ownTokens:      own.Usage.Input + own.Usage.Output,
				rollupUSD:      cost,
				rollupUnpriced: unpriced,
			})
			walk(j.ID, depth+1)
		}
	}
	walk("", 1)
	return rows
}

// sortSubagentSiblings puts any waiting-on-answer job first (it must never
// read as inert history — see TODO item 1) and otherwise preserves the
// newest-first order sortedBackgroundJobs already produced.
func sortSubagentSiblings(kids []jobs.Job) []jobs.Job {
	var waiting, rest []jobs.Job
	for _, j := range kids {
		if j.Status == jobs.StatusWaitingAnswer {
			waiting = append(waiting, j)
		} else {
			rest = append(rest, j)
		}
	}
	return append(waiting, rest...)
}

// rollupJobCost sums id's own priced cost with every descendant's
// (recursively, via byParent), and reports whether id or any descendant used
// an unpriced model. A job with no ledger usage at all (e.g. it failed
// before its first model call) is not treated as "unpriced" — only a job
// that actually spent tokens on a model the catalog cannot price counts.
func rollupJobCost(id string, byParent map[string][]jobs.Job, usage map[string]ledger.JobUsage) (float64, bool) {
	u := usage[id]
	total := u.USD
	unpriced := u.Usage != (stream.Usage{}) && !u.Priced
	for _, child := range byParent[id] {
		childUSD, childUnpriced := rollupJobCost(child.ID, byParent, usage)
		total += childUSD
		if childUnpriced {
			unpriced = true
		}
	}
	return total, unpriced
}
