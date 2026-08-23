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
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/stream"
)

// sidebarStatusCmd shows msg in the status bar for 2 seconds, mirroring
// copyFeedbackCmd's auto-clear (tui_feedback.go) — used when a sidebar
// action refuses to do something (busy, input not empty) rather than
// silently no-op'ing.
func sidebarStatusCmd(msg string) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return statusMessageClearMsg{message: msg}
	})
}

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
	m.sidebarScroll = 0
}

// closeSidebar closes the sidebar and restores scroll state.
func (m *TuiModel) closeSidebar() {
	m.sidebarActive = false
	m.sidebarCursor = 0
	m.sidebarScroll = 0
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

// sidebarSelectable reports whether the active tab has actionable rows
// (Enter/click does something row-specific) as opposed to a plain
// scrollable listing. Tokens and Lua are the latter today — Lua's rows have
// no action (no live view, no resume), so a moving cursor over them would
// just be a highlight with nothing behind it (see sidebarRowCount).
func (m TuiModel) sidebarSelectable() bool {
	switch m.sidebarTab {
	case sidebarTabSessions, sidebarTabBash, sidebarTabSubagents:
		return true
	default:
		return false
	}
}

// sidebarRowCount returns how many selectable rows the current tab has, for
// clamping sidebarCursor on Up/Down/wheel. 0 for a non-selectable tab
// (sidebarSelectable) — its content still scrolls, just without a cursor.
func (m TuiModel) sidebarRowCount() int {
	switch m.sidebarTab {
	case sidebarTabSessions:
		return len(m.sidebarSessionEntries())
	case sidebarTabBash:
		return len(m.sidebarBashJobs())
	case sidebarTabSubagents:
		return len(m.buildSubagentTree())
	default:
		return 0
	}
}

// sidebarLineCount returns how many rendered lines the active tab has right
// now, at contentWidth — the bound sidebarScroll must never exceed (past
// "the last line is at the top of the viewport").
func (m TuiModel) sidebarLineCount(contentWidth int) int {
	return len(m.sidebarTabLines(contentWidth))
}

// sidebarVisibleScroll is the ONE place sidebarScroll gets clamped to what
// the current layout can actually show — [0, max(0, lineCount-
// contentHeight)]. Both the renderer and the mouse row-click handler must
// call this rather than reading m.sidebarScroll raw: a resize (or a tab's
// content shrinking) can leave the stored value stale — e.g. scrolled to
// the bottom of a long list, then the terminal grows so the same list now
// fits without scrolling — and a renderer clamping only its own copy while
// the click handler used the raw, stale value is exactly how a click ended
// up opening the wrong row's job (an earlier review round's finding).
func (m TuiModel) sidebarVisibleScroll(layout sidebarLayoutT) int {
	lineCount := m.sidebarLineCount(layout.contentWidth)
	scroll := m.sidebarScroll
	if maxScroll := lineCount - layout.contentHeight; scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll
}

// sidebarSwitchTab changes the active tab and resets both cursor and scroll
// — a tab's row indices and line count have nothing to do with another
// tab's, so carrying either over would either select a nonsense row or open
// on a scrolled-past-the-end view.
func (m *TuiModel) sidebarSwitchTab(tab int) {
	m.sidebarTab = ((tab % sidebarTabCount) + sidebarTabCount) % sidebarTabCount
	m.sidebarCursor = 0
	m.sidebarScroll = 0
}

// sidebarClampScrollToCursor keeps sidebarCursor within the visible window
// [sidebarScroll, sidebarScroll+contentHeight), adjusting sidebarScroll by
// the minimum needed — the same "scroll just enough to reveal the cursor"
// behavior list-style pickers elsewhere in this package use.
func (m *TuiModel) sidebarClampScrollToCursor(contentHeight int) {
	if contentHeight < 1 {
		contentHeight = 1
	}
	if m.sidebarCursor < m.sidebarScroll {
		m.sidebarScroll = m.sidebarCursor
	} else if m.sidebarCursor >= m.sidebarScroll+contentHeight {
		m.sidebarScroll = m.sidebarCursor - contentHeight + 1
	}
	if m.sidebarScroll < 0 {
		m.sidebarScroll = 0
	}
}

// sidebarScrollBy moves sidebarScroll by delta, bounded to
// [0, max(0, lineCount-contentHeight)] — the plain scroll used on a
// non-selectable tab (sidebarSelectable), which has no cursor to keep in
// view but can still have more lines than fit.
func (m *TuiModel) sidebarScrollBy(delta, lineCount, contentHeight int) {
	maxScroll := lineCount - contentHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	m.sidebarScroll += delta
	if m.sidebarScroll < 0 {
		m.sidebarScroll = 0
	}
	if m.sidebarScroll > maxScroll {
		m.sidebarScroll = maxScroll
	}
}

// sidebarMoveCursor is the shared Up/Down/wheel handler: on a selectable tab
// it moves the row cursor (clamped to the row count) and scrolls just
// enough to keep it visible; on a plain scrollable tab (Tokens, Lua) there
// is no cursor, so it scrolls the view directly instead.
func (m *TuiModel) sidebarMoveCursor(delta int) {
	layout := m.sidebarLayout()
	if m.sidebarSelectable() {
		last := m.sidebarRowCount() - 1
		m.sidebarCursor += delta
		if m.sidebarCursor < 0 {
			m.sidebarCursor = 0
		}
		if m.sidebarCursor > last {
			m.sidebarCursor = max(0, last)
		}
		m.sidebarClampScrollToCursor(layout.contentHeight)
		return
	}
	m.sidebarScrollBy(delta, m.sidebarLineCount(layout.contentWidth), layout.contentHeight)
}

// ─── Update handler ─────────────────────────────────────────────────────────

func (m TuiModel) updateSidebar(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Re-clamp against the new layout immediately — otherwise a stale
		// sidebarScroll (e.g. scrolled to the bottom of a long list, then
		// the terminal grows) survives until the next cursor move, and the
		// mouse row-click handler below would use that stale value in the
		// meantime (see sidebarVisibleScroll's doc comment).
		m.sidebarScroll = m.sidebarVisibleScroll(m.sidebarLayout())
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
			m.sidebarSwitchTab(m.sidebarTab + 1)
			return m, nil

		case tea.KeyShiftTab:
			m.sidebarSwitchTab(m.sidebarTab - 1)
			return m, nil

		case tea.KeyLeft:
			m.sidebarSwitchTab(m.sidebarTab - 1)
			return m, nil

		case tea.KeyRight:
			m.sidebarSwitchTab(m.sidebarTab + 1)
			return m, nil

		case tea.KeyUp, tea.KeyCtrlUp:
			m.sidebarMoveCursor(-1)
			return m, nil

		case tea.KeyDown, tea.KeyCtrlDown:
			m.sidebarMoveCursor(1)
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
			m.sidebarMoveCursor(-1)
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			m.sidebarMoveCursor(1)
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
				if tab := sidebarTabAtX(layout, msg.X); tab >= 0 {
					m.sidebarSwitchTab(tab)
				}
				return m, nil
			}
			// Row click within content area — offset by the current scroll,
			// since row 0 on screen is sidebarVisibleScroll in the
			// underlying list once it's scrolled. Uses the clamped value,
			// not m.sidebarScroll raw, so a resize that hasn't triggered a
			// cursor move yet still maps clicks to the row actually drawn.
			if m.sidebarSelectable() && msg.Y >= layout.contentTop && msg.Y < layout.contentTop+layout.contentHeight {
				row := msg.Y - layout.contentTop + m.sidebarVisibleScroll(layout)
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

// sidebarTabAtX maps a click's absolute screen X to a tab index, using the
// SAME geometry (layout.contentLeft/contentWidth) renderSidebarTabs used to
// lay the tab row out — see sidebarLayoutT's doc comment for why both read
// these two fields instead of each re-deriving its own offset. Returns -1
// when x falls outside every tab (inside the border/padding margin, or past
// the last tab's cell).
func sidebarTabAtX(layout sidebarLayoutT, x int) int {
	rel := x - layout.contentLeft
	if rel < 0 || rel >= layout.contentWidth {
		return -1
	}
	cell := layout.contentWidth / sidebarTabCount
	if cell <= 0 {
		return -1
	}
	idx := rel / cell
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

// sidebarSubmitResume re-enters a session exactly the way a person typing
// bare "/resume" would: it submits the literal "/resume" command through
// the normal input pipeline (tui_mode.go's read loop already special-cases
// it) rather than opening a second, sidebar-local resume mechanism. The
// actual picker — sorted the same way this tab's own listing is — takes
// over from there; the picker's own cursor, not sidebarCursor, is what
// picks the session (see sidebarFooter's Sessions text, which does not
// claim otherwise).
//
// Refuses — leaving the input untouched — rather than submitting when:
// mid-turn (m.reading is false: submitting "/resume" then would either
// queue the literal string as a real user message, or land nothing useful),
// or when the input box already has something in it (typing "/resume" over
// a half-written message would silently discard it).
func (m TuiModel) sidebarSubmitResume() (tea.Model, tea.Cmd) {
	if !m.reading {
		// Short and front-loaded on purpose: buildStatus (tui_status.go)
		// hard-truncates status text to 60 columns, so anything longer lost
		// its actionable half mid-sentence.
		m.closeSidebar()
		m.statusMessage = "Press Esc first (cancels this turn), or wait, then retry."
		return m, sidebarStatusCmd(m.statusMessage)
	}
	if strings.TrimSpace(m.input.Value()) != "" {
		m.closeSidebar()
		m.statusMessage = "Clear the input first, then reopen Sessions to resume."
		return m, sidebarStatusCmd(m.statusMessage)
	}
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
//
// Refuses, without touching the input, when it already has something in it
// — this only drafts text, so silently overwriting a half-written message
// would be a pure loss with nothing gained.
func (m TuiModel) sidebarResumeSubagentRow() (tea.Model, tea.Cmd) {
	rows := m.buildSubagentTree()
	if m.sidebarCursor < 0 || m.sidebarCursor >= len(rows) {
		return m, nil
	}
	row := rows[m.sidebarCursor]
	if row.isRoot {
		return m, nil
	}
	if strings.TrimSpace(m.input.Value()) != "" {
		// See sidebarSubmitResume's identical comment on the 60-column cap.
		m.closeSidebar()
		m.statusMessage = "Clear the input first, then press r again to draft it."
		return m, sidebarStatusCmd(m.statusMessage)
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
//
// A subagent whose ParentID does not point at another tracked subagent — its
// parent was a non-subagent job (e.g. a cron run), or its parent has since
// been evicted from backgroundJobs (see pruneBackgroundJobsLocked) — is not
// reachable from the synthetic root by the normal walk. Rather than let such
// a job silently vanish from the tree, every byParent group whose key the
// walk never visited is emitted afterward as its own depth-1 root (still
// walked recursively, so its own children render at the correct relative
// depth).
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

	// "" (the synthetic root's own key) gets marked visited by the very
	// first walk("", 1) call below, same as every other key — nothing here
	// pre-seeds it, or the cycle guard inside walk (visited[parentID] ==
	// true means "already done, return") would make that first call a
	// no-op before it ever ran.
	visited := map[string]bool{}
	var walk func(parentID string, depth int)
	walk = func(parentID string, depth int) {
		// Guards two things with the same check: (1) a ParentID cycle
		// (A's parent is B, B's parent is A) would otherwise recurse
		// forever — practically unreachable today since a job's ParentID
		// is only ever set to an already-existing job at spawn time, but
		// cheap to close off; (2) makes this function itself idempotent, so
		// the orphan-emission loop below can call it without separately
		// re-deriving whether a given key was already covered by an
		// earlier orphan's walk.
		if visited[parentID] {
			return
		}
		visited[parentID] = true
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

	// Orphan groups: parent keys the walk above never reached. Sorted so
	// output is deterministic across calls (map iteration order is not).
	var orphanParents []string
	for parent := range byParent {
		if !visited[parent] {
			orphanParents = append(orphanParents, parent)
		}
	}
	sort.Strings(orphanParents)
	for _, parent := range orphanParents {
		// orphanParents was collected once, before any orphan walk ran, so
		// it can list two keys from the SAME parent chain (e.g. group "P"
		// contains job A, and "A" is itself a byParent key because some job
		// B has ParentID=A). Walking "P" first already reaches "A" via the
		// normal recursive walk(j.ID, depth+1) call and marks it visited —
		// walking "A" again here would then re-add B a second time, once as
		// a child of A (correct) and once more as if A were its own root.
		if visited[parent] {
			continue
		}
		walk(parent, 1)
	}

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
//
// Public signature unchanged (existing tests call it directly with 3 args);
// the cycle guard lives in the unexported helper below, given a fresh
// visited set per top-level call.
func rollupJobCost(id string, byParent map[string][]jobs.Job, usage map[string]ledger.JobUsage) (float64, bool) {
	return rollupJobCostVisited(id, byParent, usage, map[string]bool{})
}

// rollupJobCostVisited is rollupJobCost's actual recursion, with a cycle
// guard: a ParentID loop (A's parent is B, B's parent is A) would otherwise
// recurse forever, the same hazard buildSubagentTree's walk closure guards
// against for the same reason — practically unreachable today (ParentID is
// only ever set to an already-existing job at spawn time), but cheap to
// close off here too since this recurses over the same byParent structure
// independently of walk.
func rollupJobCostVisited(id string, byParent map[string][]jobs.Job, usage map[string]ledger.JobUsage, visited map[string]bool) (float64, bool) {
	if visited[id] {
		return 0, false
	}
	visited[id] = true
	u := usage[id]
	total := u.USD
	unpriced := u.Usage != (stream.Usage{}) && !u.Priced
	for _, child := range byParent[id] {
		childUSD, childUnpriced := rollupJobCostVisited(child.ID, byParent, usage, visited)
		total += childUSD
		if childUnpriced {
			unpriced = true
		}
	}
	return total, unpriced
}
