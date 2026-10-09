package display

// The right-side sidebar (TODO.md item 1): Tokens / Sessions / Tasks tabs,
// toggled with Ctrl+T or by clicking the status bar's
// context figure (see tui_status.go's statusRightHit).
//
// Unlike the jobs modal / todo modal / /btw modal / resume picker in this
// file set, this is NOT a full-screen overlay: it renders as a live column
// alongside a narrowed main conversation (see tui_sidebar_view.go's
// mainColumnWidth/sidebarLayout and tui_view.go's renderFrame), so both stay
// visible and interactive at once. That makes keyboard focus ambiguous —
// there are now two things on screen that could reasonably want the
// keyboard — so the sidebar tracks its own focus state (m.sidebarFocused,
// tui.go): opening it defaults focus to the conversation (typing lands in
// the input box as normal). Ctrl+Right or Shift+Tab from the conversation
// "walks into" the sidebar's tabs; Ctrl+Left, Ctrl+Right or Shift+Tab walk
// back out to the conversation from any tab. Shift+Tab also opens a closed
// sidebar. Plain Left/Right are untouched and keep switching sidebar
// tabs (and moving the prompt cursor, since they are never hijacked). See
// Update() in tui_update.go for the routing this drives and updateSidebar
// below for the focus-exit logic.
//
// Data sources, deliberately reused rather than duplicated:
//   - Tokens: buildUsageDetail (tui_tokens.go), previously dead code (Inbox
//     item F11) — this is its first production caller.
//   - Sessions: session.ResumeEntries via TUI.SetSessionLister, the same
//     source bare "/resume" already uses. Re-entering a session goes through
//     the exact same path a typed "/resume" would (see sidebarSubmitResume):
//     no second resume mechanism.
//   - Bash / Subagents: jobs.Job rows already mirrored into
//     m.backgroundJobs by TUI.SetJobEvents, filtered on the Kind field
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
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/crazy-goat/tyci-agent/internal/ledger"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/crazy-goat/tyci-agent/tools"
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
	sidebarTabTasks
	sidebarTabRuns
	sidebarTabCount
)

var sidebarTabNames = [sidebarTabCount]string{
	sidebarTabTokens:   "Tokens",
	sidebarTabSessions: "Sessions",
	sidebarTabTasks:    "Tasks",
	sidebarTabRuns:     "Runs",
}

// sidebarTabLabel is the text of one tab in the tab row: its name with one
// space of padding on each side. renderSidebarTabs and sidebarTabAtX both
// read the width from here.
func sidebarTabLabel(tab int) string {
	return " " + sidebarTabNames[tab] + " "
}

// sidebarTabStart is the column offset of tab's label in the tab row.
func sidebarTabStart(tab int) int {
	start := 0
	for i := 0; i < tab; i++ {
		start += len(sidebarTabLabel(i))
	}
	return start
}

// openSidebar opens the sidebar on the given tab, saving scroll state the
// same way every other full-screen overlay in this package does. Focus
// always starts on the conversation (sidebarFocused = false), never
// reopening with the keyboard already captured by the tab list.
//
// Opening (or closing, in closeSidebar below) changes the effective width
// the transcript wraps at — the main column narrows to make room for this
// column. getBlockLines (tui_render_block.go) caches each block's wrapped
// lines and only re-wraps when something explicitly invalidates that cache
// (normally a real terminal resize, via handleResizeFlush); it does not
// itself compare against the current width. Without the
// invalidateAllBlockLineCounts call below, toggling the sidebar would leave
// every already-rendered block wrapped at the old (wrong) width — the outer
// message-region cache (buildMessageRegionCached) WOULD notice its width
// changed and rebuild, but it would rebuild from these stale per-block
// lines, so the transcript would keep showing yesterday's wrap. This is the
// one piece of "reuse the existing width-keyed caches for free" that does
// not hold automatically and needed an explicit fix.
func (m *TuiModel) openSidebar(tab int) {
	if tab < 0 || tab >= sidebarTabCount {
		tab = sidebarTabTokens
	}
	wasActive := m.sidebarActive
	if !wasActive {
		m.savedScrollLine = m.scrollLine
		m.savedAtBottom = m.atBottom
	}
	m.sidebarActive = true
	m.sidebarFocused = false
	m.sidebarTab = tab
	m.sidebarCursor = 0
	m.sidebarScroll = 0
	m.sidebarTaskOwner = ""
	if !wasActive {
		// Only an actual closed->open transition changes the effective
		// width the transcript wraps at (mainColumnWidth narrows). Calling
		// this again while already active (e.g. clicking the status bar's
		// context figure to jump to the Tokens tab while some other tab is
		// already open) would just re-wrap everything for no reason.
		m.invalidateAllBlockLineCounts()
		// F20: fix the REAL m.input's width here, not just the copy
		// renderFrame narrows for the one frame it draws (tui_view.go).
		// Without this, m.input.Width() stays at the pre-open, full-terminal
		// value until the next keystroke recomputes it, so the textarea's
		// own reported height (used to size the input area) is briefly
		// wrong for the now-narrower column. SetWidth alone does not
		// recompute the row count a review round caught this missing: a
		// ~200-char prompt at width=120 needs 4 wrapped rows once narrowed
		// to mainColumnWidth()=71, but stayed rendered at the pre-narrow 2
		// rows until the next keystroke happened to call capInputHeight —
		// the exact symptom F20 was filed for, still present after only
		// fixing the width.
		m.input.SetWidth(max(10, m.mainColumnWidth()-2))
		m.capInputHeight()
	}
}

// closeSidebar closes the sidebar and restores scroll state. See
// openSidebar's doc comment for why the effective transcript width changing
// back to full-screen needs the same explicit block-line-cache invalidation.
func (m *TuiModel) closeSidebar() {
	// The agent view needs the sidebar for its way back (Enter on main), so
	// closing the sidebar always ends the view.
	m.closeAgentView()
	m.sidebarActive = false
	m.sidebarFocused = false
	m.sidebarCursor = 0
	m.sidebarScroll = 0
	m.sidebarTaskOwner = ""
	m.atBottom = m.savedAtBottom
	m.scrollLine = m.savedScrollLine
	m.selectionVersion++
	m.selection = SelectionState{}
	m.selectionFlash = false
	m.invalidateAllBlockLineCounts()
	// F20's other direction: restore the REAL m.input's width back to the
	// full terminal width immediately, symmetric with openSidebar's fix —
	// sidebarActive is already false above, so mainColumnWidth() now
	// returns m.width. capInputHeight follows for the same reason as
	// openSidebar's call: SetWidth alone never recomputes the row count.
	m.input.SetWidth(max(10, m.mainColumnWidth()-2))
	m.capInputHeight()
}

// toggleSidebar is Ctrl+T's handler: close if open, otherwise reopen on
// whichever tab was last selected (Tokens, the zero value, the first time).
func (m *TuiModel) toggleSidebar() {
	if m.sidebarActive {
		m.closeSidebarPersisted()
		return
	}
	m.openSidebar(m.sidebarTab)
	m.persistSidebarVisible(true)
}

// closeSidebarPersisted closes the sidebar AND persists the closed state —
// for the user-driven exits (Ctrl+T, Esc, click-away): the user chose to hide
// it, so a restart must not resurrect it.
func (m *TuiModel) closeSidebarPersisted() {
	wasActive := m.sidebarActive
	m.closeSidebar()
	if wasActive {
		m.persistSidebarVisible(false)
	}
}

// sidebarSelectable reports whether the active tab has actionable rows
// (Enter/click does something row-specific) as opposed to a plain
// scrollable listing. Tokens is the latter today — its rows have no action,
// so a moving cursor over them would just be a highlight with nothing behind
// it (see sidebarRowCount).
func (m TuiModel) sidebarSelectable() bool {
	switch m.sidebarTab {
	case sidebarTabSessions, sidebarTabTasks, sidebarTabRuns:
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
	case sidebarTabTasks:
		return len(m.sidebarTaskJobRows(m.sidebarLayout().contentWidth))
	case sidebarTabRuns:
		return len(m.sidebarRunRows())
	default:
		return 0
	}
}

// sidebarLineCount returns how many rendered lines the active tab has right
// now, at contentWidth — the bound sidebarScroll must never exceed (past
// "the last line is at the top of the viewport").
func (m TuiModel) sidebarLineCount(contentWidth int) int {
	if m.sidebarTab == sidebarTabTasks {
		// renderSidebarTasks emits one line per row, and the rows always start
		// with a heading. Counting the rows skips the styling of every line.
		return len(m.sidebarTaskRows(contentWidth))
	}
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
	return m.sidebarVisibleScrollForLineCount(layout, m.sidebarLineCount(layout.contentWidth))
}

// sidebarVisibleScrollForLineCount clamps sidebarScroll using content that has
// already been rendered by the caller. Keeping the line count as an argument
// avoids rebuilding a tab's full content just to calculate its scroll bound.
func (m TuiModel) sidebarVisibleScrollForLineCount(layout sidebarLayoutT, lineCount int) int {
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
	m.sidebarTaskOwner = ""
	m.sidebarStopRun = ""
}

// sidebarClampScrollToCursor keeps sidebarCursor within the visible window
// [sidebarScroll, sidebarScroll+contentHeight), adjusting sidebarScroll by
// the minimum needed — the same "scroll just enough to reveal the cursor"
// behavior list-style pickers elsewhere in this package use.
func (m *TuiModel) sidebarClampScrollToCursor(contentHeight int) {
	cursorLine := m.sidebarCursor
	switch m.sidebarTab {
	case sidebarTabTasks:
		_, jobRows := m.sidebarTaskRowsAndJobs(m.sidebarLayout().contentWidth)
		if m.sidebarCursor < 0 || m.sidebarCursor >= len(jobRows) {
			return
		}
		cursorLine = jobRows[m.sidebarCursor]
	case sidebarTabRuns:
		// A run takes one line or more; the cursor follows its first line.
		starts := m.runsTab(m.sidebarLayout().contentWidth).start
		if m.sidebarCursor < 0 || m.sidebarCursor >= len(starts) {
			return
		}
		cursorLine = starts[m.sidebarCursor]
	}
	m.sidebarScrollToLine(cursorLine, contentHeight)
}

// sidebarScrollToLine moves sidebarScroll the minimum needed to keep the
// content line cursorLine inside the window of contentHeight lines.
func (m *TuiModel) sidebarScrollToLine(cursorLine, contentHeight int) {
	if contentHeight < 1 {
		contentHeight = 1
	}
	if cursorLine < m.sidebarScroll {
		m.sidebarScroll = cursorLine
	} else if cursorLine >= m.sidebarScroll+contentHeight {
		m.sidebarScroll = cursorLine - contentHeight + 1
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
		if m.sidebarTab == sidebarTabTasks {
			_, jobRows := m.sidebarTaskRowsAndJobs(layout.contentWidth)
			jobCount := len(jobRows)
			if jobCount == 0 {
				return
			}
			m.sidebarCursor += delta
			if m.sidebarCursor < 0 {
				m.sidebarCursor = 0
			}
			if m.sidebarCursor >= jobCount {
				m.sidebarCursor = jobCount - 1
			}
			m.sidebarScrollToLine(jobRows[m.sidebarCursor], layout.contentHeight)
			return
		} else {
			last := m.sidebarRowCount() - 1
			m.sidebarCursor += delta
			if m.sidebarCursor < 0 {
				m.sidebarCursor = 0
			}
			if m.sidebarCursor > last {
				m.sidebarCursor = max(0, last)
			}
		}
		m.sidebarClampScrollToCursor(layout.contentHeight)
		return
	}
	m.sidebarScrollBy(delta, m.sidebarLineCount(layout.contentWidth), layout.contentHeight)
}

// ─── Update handler ─────────────────────────────────────────────────────────

// routeSidebarMsg is Update()'s entry point while m.sidebarActive: it
// decides, per message, whether the sidebar or the (still-visible, still-
// interactive) main conversation owns it. handled=false means "not for me,
// fall through to the normal main-conversation handling" — Update() does
// that itself, so this never needs to know how to run that path.
//
//   - WindowSizeMsg always comes here first regardless of focus: both the
//     main model's resize bookkeeping (handleResize's debounce, input width)
//     and the sidebar's own scroll re-clamp need to happen on every resize.
//   - MouseMsg is routed by physical column (msg.X vs mainColumnWidth()),
//     regardless of focus — a click is itself where the user's attention
//     just went, so it also updates sidebarFocused to match which side was
//     clicked.
//   - KeyMsg goes to the sidebar only while sidebarFocused; otherwise only
//     Ctrl+Right and Shift+Tab are claimed here (entering focus) and
//     everything else falls through to the normal keymap. Shift+Tab is not
//     claimed while the subagent modal is open: that modal is drawn above
//     the sidebar, so the key belongs to it. Tab is never claimed here,
//     because the terminal treats Tab as a focus-cycle key; the sidebar's
//     tabs are driven by arrows only.
//   - tuiMsgBlock never reaches this function: Update() dispatches it to
//     handleBlockMsg before any sidebar routing, so streamed blocks keep
//     flowing whether or not the sidebar has focus.
//   - Everything else (ticks, …) is claimed only while focused; unfocused,
//     it falls through so the conversation keeps updating live even with
//     the sidebar open.
func (m TuiModel) routeSidebarMsg(msg tea.Msg) (handled bool, model tea.Model, cmd tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		newModel, cmd := m.handleResize(msg)
		nm := newModel.(TuiModel)
		nm.sidebarScroll = nm.sidebarVisibleScroll(nm.sidebarLayout())
		return true, nm, cmd

	case tea.MouseMsg:
		if msg.X < m.mainColumnWidth() {
			// dispatchShadow() (tui_sidebar_view.go), NOT mainShadow():
			// this used to go through mainShadow(), which clears
			// sidebarActive for width purposes — correct for rendering, but
			// wrong here, because dispatching a message can run a handler
			// that reads sidebarActive as real UI state. Concretely, a
			// click on the status bar's context figure routes here and
			// calls openSidebar, which used mainShadow()'s falsified
			// sidebarActive=false to conclude the sidebar was CLOSED and
			// ran its whole open-transition on an already-open sidebar:
			// double-narrowed input width, clobbered saved scroll position,
			// a full cache invalidation — all from one click (review of
			// F13's mainShadow() fix). dispatchShadow narrows width via
			// widthFinal instead, leaving sidebarActive true and
			// accurate for any handler that reads it.
			mainM := m.dispatchShadow()
			mainM.sidebarFocused = false
			newModel, cmd := mainM.handleMouseMsg(msg)
			result := newModel.(TuiModel)
			result.width = m.width // restore the real (unnarrowed) width
			result.widthFinal = false
			return true, result, cmd
		}
		m.sidebarFocused = true
		model, cmd := m.updateSidebar(msg)
		return true, model, cmd

	case tea.KeyMsg:
		if m.sidebarFocused {
			model, cmd := m.updateSidebar(msg)
			return true, model, cmd
		}
		// Ctrl+Right and Shift+Tab walk focus "into" the sidebar from the
		// conversation side, landing on whichever tab was already selected
		// (not reset to 0) — see tui_sidebar.go's package doc comment. Plain
		// Right is left alone so it keeps moving the cursor through the
		// prompt text (it was previously hijacked for this, which made it
		// impossible to move the prompt cursor right while the sidebar was
		// open).
		if msg.Type == tea.KeyCtrlRight {
			m.sidebarFocused = true
			return true, m, nil
		}
		if msg.Type == tea.KeyShiftTab && !m.subagentModalActive {
			m.sidebarFocused = true
			return true, m, nil
		}
		// Shift+Left makes the sidebar wider and Shift+Right narrower, also
		// with the input focused. The modal above keeps these keys.
		if (msg.Type == tea.KeyShiftLeft || msg.Type == tea.KeyShiftRight) && !m.subagentModalActive {
			delta := 1
			if msg.Type == tea.KeyShiftRight {
				delta = -1
			}
			next, cmd := m.resizeSidebar(delta)
			return true, next, cmd
		}
		return false, m, nil

	default:
		if m.sidebarFocused {
			model, cmd := m.updateSidebar(msg)
			return true, model, cmd
		}
		return false, m, nil
	}
}

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
		// A pending stop of a run waits for y. Any other key cancels it.
		if m.sidebarStopRun != "" {
			run := m.sidebarStopRun
			m.sidebarStopRun = ""
			if msg.Type == tea.KeyRunes && string(msg.Runes) == "y" {
				return m, m.stopRunCmd(run)
			}
		}
		switch msg.Type {
		case tea.KeyEscape:
			if m.agentView != nil {
				m.closeAgentView()
				return m, nil
			}
			m.closeSidebarPersisted()
			return m, nil

		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit

		case tea.KeyCtrlT:
			m.closeSidebarPersisted()
			return m, nil

		case tea.KeyCtrlLeft, tea.KeyCtrlRight, tea.KeyShiftTab:
			// Ctrl+Left, Ctrl+Right and Shift+Tab always walk focus back OUT
			// to the conversation, from any tab, regardless of position.
			// Ctrl+Right and Shift+Tab are the keys that enter the sidebar
			// (routeSidebarMsg), so these are their inverse.
			m.sidebarFocused = false
			return m, nil

		case tea.KeyLeft:
			// At the leftmost tab, Left walks focus back OUT to the
			// conversation instead of wrapping to the last tab — this case
			// is only reached while sidebarFocused (Update() gates it), so
			// it's always a focused-navigation key, never a boundary no-op.
			if m.sidebarTab == 0 {
				m.sidebarFocused = false
				return m, nil
			}
			m.sidebarSwitchTab(m.sidebarTab - 1)
			return m, m.armStatusTick()

		case tea.KeyRight:
			// Symmetric exit at the rightmost tab.
			if m.sidebarTab == sidebarTabCount-1 {
				m.sidebarFocused = false
				return m, nil
			}
			m.sidebarSwitchTab(m.sidebarTab + 1)
			return m, m.armStatusTick()

		case tea.KeyUp, tea.KeyCtrlUp:
			m.sidebarMoveCursor(-1)
			return m, nil

		case tea.KeyDown, tea.KeyCtrlDown:
			m.sidebarMoveCursor(1)
			return m, nil

		case tea.KeyEnter:
			return m.sidebarActivateRow()

		case tea.KeyShiftLeft, tea.KeyShiftRight:
			delta := 1
			if msg.Type == tea.KeyShiftRight {
				delta = -1
			}
			return m.resizeSidebar(delta)

		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "r":
				if m.sidebarTab == sidebarTabTasks {
					return m.sidebarResumeSubagentRow()
				}
			case "x":
				if m.sidebarTab == sidebarTabRuns {
					return m.sidebarAskStop(), nil
				}
			case "<":
				// Fallback for terminals that do not send Shift+Left.
				return m.resizeSidebar(1)
			case ">":
				return m.resizeSidebar(-1)
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
				m.closeSidebarPersisted()
				return m, nil
			}
			// Tab row click.
			if msg.Y == layout.top+1 {
				if tab := sidebarTabAtX(layout, msg.X); tab >= 0 {
					m.sidebarSwitchTab(tab)
				}
				return m, m.armStatusTick()
			}
			// Row click within content area — offset by the current scroll,
			// since row 0 on screen is sidebarVisibleScroll in the
			// underlying list once it's scrolled. Uses the clamped value,
			// not m.sidebarScroll raw, so a resize that hasn't triggered a
			// cursor move yet still maps clicks to the row actually drawn.
			//
			// `line` is an index into the RENDERED LINE list (one line per
			// sidebarTaskRow — renderSidebarTasks emits exactly one styled
			// line per row, headings included), computed once here. It is
			// NOT the same thing as m.sidebarRowCount(), which on Tasks
			// counts only job rows (see its doc comment) — Tasks mixes
			// headings, a job-less synthetic root, and job rows into that
			// one line-per-row list, so bounding the click against the job
			// count instead of the actual line count rejected clicks on
			// later job rows once enough headings preceded them (F3).
			// Bounding against the real line count (sidebarTaskRows' own
			// length) and finding the nearest job row at-or-after the
			// clicked line — rather than assuming line index == job index —
			// is what makes clicking a heading fall through to the next job
			// below it instead of misfiring on an unrelated job or nothing.
			if m.sidebarSelectable() && msg.Y >= layout.contentTop && msg.Y < layout.contentTop+layout.contentHeight {
				// Tasks builds its rows once here, for the scroll bound and the
				// row lookup below.
				var taskRows []sidebarTaskRow
				var taskJobRows []int
				scroll := 0
				if m.sidebarTab == sidebarTabTasks {
					taskRows, taskJobRows = m.sidebarTaskRowsAndJobs(layout.contentWidth)
					scroll = m.sidebarVisibleScrollForLineCount(layout, len(taskRows))
				} else {
					scroll = m.sidebarVisibleScroll(layout)
				}
				line := msg.Y - layout.contentTop + scroll
				switch m.sidebarTab {
				case sidebarTabTasks:
					rows := taskRows
					// A separator selects nothing. Unlike a heading, it does not
					// fall through to the next job row.
					if line < 0 || line >= len(rows) || rows[line].isSeparator {
						return m, nil
					}
					jobRows := taskJobRows
					selected := -1
					for i, jobRow := range jobRows {
						if jobRow >= line {
							selected = i
							break
						}
					}
					if selected >= 0 {
						m.sidebarCursor = selected
						return m.sidebarActivateRow()
					}
				case sidebarTabRuns:
					// A run has one line or more; a click on any of them
					// toggles that run. The separator belongs to no run.
					owner := m.runsTab(layout.contentWidth).owner
					if line >= 0 && line < len(owner) && owner[line] >= 0 {
						m.sidebarCursor = owner[line]
						return m.sidebarActivateRow()
					}
				default:
					if line >= 0 && line < m.sidebarRowCount() {
						m.sidebarCursor = line
						return m.sidebarActivateRow()
					}
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
// the last tab's label).
func sidebarTabAtX(layout sidebarLayoutT, x int) int {
	rel := x - layout.contentLeft
	if rel < 0 || rel >= layout.contentWidth {
		return -1
	}
	for tab := 0; tab < sidebarTabCount; tab++ {
		if rel < sidebarTabStart(tab)+len(sidebarTabLabel(tab)) {
			return tab
		}
	}
	return -1
}

// sidebarActivateRow is Enter's (and a row click's) handler: what "open
// this row" means depends on the active tab. On the Tasks tab, a Subagents
// row selects its agent, so the Bash and Lua rows list that agent's jobs. The
// main row selects main. A Bash row keeps the selection. closeSidebar below
// clears the selection again, so for a subagent without a live transcript the
// Bash and Lua rows show main once the sidebar closes.
func (m TuiModel) sidebarActivateRow() (tea.Model, tea.Cmd) {
	switch m.sidebarTab {
	case sidebarTabSessions:
		return m.sidebarSubmitResume()
	case sidebarTabTasks:
		rows, jobRows := m.sidebarTaskRowsAndJobs(m.sidebarLayout().contentWidth)
		if m.sidebarCursor >= 0 && m.sidebarCursor < len(jobRows) {
			row := rows[jobRows[m.sidebarCursor]]
			if row.isMain {
				m.sidebarTaskOwner = ""
			} else if row.subagent {
				m.sidebarTaskOwner = row.job.ID
			}
			switch {
			case row.isMain:
				m.closeAgentView()
			case row.subagent && m.openAgentView(row.job.ID, row.job.Description):
				// The sidebar stays open, so Enter on main can bring the
				// main conversation back. The view follows the agent on the
				// status tick, so the tick chain must run from here on.
				cmd := m.armStatusTick()
				return m, cmd
			case row.job != nil:
				m.closeSidebar()
				m.openJobResultModal(*row.job)
			}
		}
		return m, nil
	case sidebarTabRuns:
		return m.sidebarToggleRun(), nil
	default:
		return m, nil
	}
}

// sidebarAskStop asks for y before the run under the cursor is stopped. Only a
// running run can be stopped: Manager.Stop rejects the other runs.
func (m TuiModel) sidebarAskStop() TuiModel {
	rows := m.sidebarRunRows()
	if m.sidebarCursor < 0 || m.sidebarCursor >= len(rows) || rows[m.sidebarCursor].Status != "running" {
		return m
	}
	m.sidebarStopRun = rows[m.sidebarCursor].ID
	return m
}

// stopRunCmd stops a run in a background command. Manager.Stop can wait for
// seconds, so it must not run in Update. The result comes back as runStopResultMsg.
func (m TuiModel) stopRunCmd(run string) tea.Cmd {
	stop := m.runStopper
	if stop == nil {
		return nil
	}
	return func() tea.Msg { return runStopResultMsg{run: run, err: stop(run)} }
}

// sidebarToggleRun expands or collapses the run under the cursor.
func (m TuiModel) sidebarToggleRun() TuiModel {
	rows := m.sidebarRunRows()
	if m.sidebarCursor < 0 || m.sidebarCursor >= len(rows) {
		return m
	}
	id := rows[m.sidebarCursor].ID
	if m.sidebarRunsExpanded == nil {
		m.sidebarRunsExpanded = map[string]bool{}
	}
	m.sidebarRunsExpanded[id] = !m.sidebarRunsExpanded[id]
	return m
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
	next := m.submit().(TuiModel)
	return next, next.armStatusTick()
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
	rows, jobRows := m.sidebarTaskRowsAndJobs(m.sidebarLayout().contentWidth)
	if m.sidebarCursor < 0 || m.sidebarCursor >= len(jobRows) || rows[jobRows[m.sidebarCursor]].job == nil || !rows[jobRows[m.sidebarCursor]].subagent {
		return m, nil
	}
	row := *rows[jobRows[m.sidebarCursor]].job
	if strings.TrimSpace(m.input.Value()) != "" {
		// See sidebarSubmitResume's identical comment on the 60-column cap.
		m.closeSidebar()
		m.statusMessage = "Clear the input first, then press r again to draft it."
		return m, sidebarStatusCmd(m.statusMessage)
	}
	m.closeSidebar()
	m.input.SetValue(fmt.Sprintf("Use the resume tool to continue job %s (%s): ",
		jobs.ShortID(row.ID), truncateString(row.Description, 40)))
	return m, nil
}

// ─── Data sources ───────────────────────────────────────────────────────────

// sidebarSessionsTTL is how old the Sessions tab list may get before the next
// message starts a new load (see sidebarSessionsCmd).
const sidebarSessionsTTL = 5 * time.Second

// sidebarSessionsMsg carries one finished load of the Sessions tab list,
// newest-first.
type sidebarSessionsMsg struct {
	entries []TuiResumeEntry
}

// sidebarSessionsCmd starts a load of the Sessions tab list when that tab is
// on screen, the list is older than sidebarSessionsTTL and no load is in
// flight. The lister reads the session files, so the load runs in the
// returned command, off the Bubble Tea goroutine; View reads only the cached
// list. Update calls this on the model it returns.
func (m *TuiModel) sidebarSessionsCmd() tea.Cmd {
	if m.sessionLister == nil || m.sessionsLoading || !m.sidebarActive || m.sidebarTab != sidebarTabSessions {
		return nil
	}
	if !m.sessionsLoadedAt.IsZero() && time.Since(m.sessionsLoadedAt) < sidebarSessionsTTL {
		return nil
	}
	m.sessionsLoading = true
	lister := m.sessionLister
	return func() tea.Msg {
		entries := lister()
		sorted := make([]TuiResumeEntry, len(entries))
		copy(sorted, entries)
		sort.SliceStable(sorted, func(i, j int) bool {
			return sorted[i].ModTime.After(sorted[j].ModTime)
		})
		return sidebarSessionsMsg{entries: sorted}
	}
}

// sidebarSessionEntries returns the cached Sessions tab list, newest-first.
// It is empty until the first load arrives (see sidebarSessionsCmd).
func (m TuiModel) sidebarSessionEntries() []TuiResumeEntry {
	return m.sessionEntries
}

// sidebarBashJobs returns the backgrounded bash jobs (jobs.KindBash) that the
// agent sidebarTaskOwner started, oldest first, for the Bash tab. all is the
// newest-first list from sortedBackgroundJobs, so the caller sorts once.
func (m TuiModel) sidebarBashJobs(all []jobs.Job) []jobs.Job {
	newestFirst := all
	subagents := sidebarSubagentIDs(newestFirst)
	var out []jobs.Job
	for i := len(newestFirst) - 1; i >= 0; i-- {
		if newestFirst[i].Kind == jobs.KindBash && sidebarOwnedBy(newestFirst[i].ParentID, m.sidebarTaskOwner, subagents) {
			out = append(out, newestFirst[i])
		}
	}
	return out
}

// sidebarSubagentIDs returns the job ids of the subagents in list. They are
// the Subagents rows of the Tasks tab.
func sidebarSubagentIDs(list []jobs.Job) map[string]bool {
	ids := map[string]bool{}
	for _, j := range list {
		if j.Kind == jobs.KindSubagent {
			ids[j.ID] = true
		}
	}
	return ids
}

// sidebarOwnedBy reports whether owner, the job id of the agent that started
// a Bash job or a Lua run, belongs to the selected agent. The main
// conversation (selected "") also owns every owner that has no Subagents row.
// Such an owner was evicted from the job list, but a job it started can still
// run, and it must not vanish from the Tasks tab.
func sidebarOwnedBy(owner, selected string, subagents map[string]bool) bool {
	if selected == "" {
		return !subagents[owner]
	}
	return owner == selected
}

// sidebarTaskRow is one rendered line in Tasks. Group headings are not
// selectable; job rows retain pointers to the existing job records.
type sidebarTaskRow struct {
	group     string
	line      string
	job       *jobs.Job
	isHeading bool
	subagent  bool
	// isMain marks the synthetic "main" row: the main conversation, which
	// is selectable but is not a job.
	isMain bool
	// isSeparator marks the line between the active and the finished
	// subagents of a sibling group. Like a heading, it is not selectable.
	isSeparator bool
}

// sidebarTaskRowBuilds counts the calls of sidebarTaskRows. The tests read it
// to check how many times one key, click or job event builds the Tasks rows.
var sidebarTaskRowBuilds atomic.Int64

// sidebarTaskRows keeps the three source groups separate and stable. The Bash
// jobs and the Lua runs are each oldest first at the point they are read.
func (m TuiModel) sidebarTaskRows(width int) []sidebarTaskRow {
	sidebarTaskRowBuilds.Add(1)
	// The job map is sorted once here and shared by the three source groups.
	all := m.sortedBackgroundJobs()
	rows := []sidebarTaskRow{{group: "Subagents", line: "Subagents", isHeading: true}}
	tree := m.buildSubagentTreeFrom(all)
	tokW, costW := subagentColumnWidths(tree)
	for _, treeRow := range tree {
		if treeRow.separatorBefore {
			line := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(strings.Repeat("─", width))
			rows = append(rows, sidebarTaskRow{group: "Subagents", line: line, isSeparator: true})
		}
		row := sidebarTaskRow{group: "Subagents", line: m.formatSubagentRow(treeRow, width, tokW, costW), isMain: treeRow.isRoot}
		if !treeRow.isRoot {
			job := treeRow.job
			row.job = &job
			row.subagent = true
		}
		rows = append(rows, row)
	}

	rows = append(rows, sidebarTaskRow{group: "Bash", line: "Bash", isHeading: true})
	bash := m.sidebarBashJobs(all)
	idWidth := 0
	for _, job := range bash {
		idWidth = max(idWidth, lipgloss.Width(shortJobID(job.ID)))
	}
	for _, job := range bash {
		job := job
		rows = append(rows, sidebarTaskRow{group: "Bash", line: " " + sidebarJobLine(job, max(1, width-1), idWidth), job: &job})
	}

	rows = append(rows, sidebarTaskRow{group: "Lua", line: "Lua", isHeading: true})
	rows = append(rows, sidebarLuaRows(m.sidebarOwnedLuaRuns(all), width)...)
	return rows
}

// sidebarOwnedLuaRuns returns the Lua runs that the agent sidebarTaskOwner
// ran, oldest first, for the Lua rows of the Tasks tab. all is the list from
// sortedBackgroundJobs, so the caller sorts once.
func (m TuiModel) sidebarOwnedLuaRuns(all []jobs.Job) []tools.LuaRun {
	subagents := sidebarSubagentIDs(all)
	var out []tools.LuaRun
	for _, r := range tools.LuaRunHistory() {
		if sidebarOwnedBy(r.Owner, m.sidebarTaskOwner, subagents) {
			out = append(out, r)
		}
	}
	return out
}

// sidebarLuaRows returns one Tasks row per Lua run in history, which is
// oldest first. The duration sits at the right edge. The name is cut first
// when the sidebar is narrow.
func sidebarLuaRows(history []tools.LuaRun, width int) []sidebarTaskRow {
	// The duration column is as wide as the widest duration in history, so
	// the age column lines up on every row.
	durW := 0
	for _, r := range history {
		durW = max(durW, lipgloss.Width(r.Duration.Round(time.Millisecond).String()))
	}
	// The name takes at most 20 cells. The cells around it are " ✓ " (3),
	// a space (1), the age (6), " ago" (4) and the gap before the duration (1).
	nameW := min(20, max(1, width-15-durW))
	var rows []sidebarTaskRow
	for _, r := range history {
		icon := "✓"
		if !r.Success {
			icon = "✗"
		}
		left := fmt.Sprintf(" %s %-*s %6s ago", icon,
			nameW, truncateString(r.Name, nameW), formatDurationShort(time.Since(r.StartedAt)))
		rows = append(rows, sidebarTaskRow{group: "Lua", line: lineWithRight(left, r.Duration.Round(time.Millisecond).String(), width)})
	}
	return rows
}

func (m TuiModel) sidebarTaskJobRows(width int) []int {
	return sidebarJobRowIndices(m.sidebarTaskRows(width))
}

// sidebarTaskRowsAndJobs builds the Tasks rows once and returns them with the
// indices of their job rows. Handlers that need both use it instead of
// calling sidebarTaskRows and sidebarTaskJobRows, which build the rows twice.
func (m TuiModel) sidebarTaskRowsAndJobs(width int) ([]sidebarTaskRow, []int) {
	rows := m.sidebarTaskRows(width)
	return rows, sidebarJobRowIndices(rows)
}

// sidebarJobRowIndices returns the indices in rows of the selectable job rows:
// the job rows and the main row.
func sidebarJobRowIndices(rows []sidebarTaskRow) []int {
	indices := make([]int, 0)
	for i, row := range rows {
		if row.job != nil || row.isMain {
			indices = append(indices, i)
		}
	}
	return indices
}

// sidebarCursorJobID returns the ID of the job under the Tasks cursor. It
// returns "" when the Tasks tab is not open, or when the cursor is on the
// main row, which is not a job.
func (m TuiModel) sidebarCursorJobID() string {
	if !m.sidebarActive || m.sidebarTab != sidebarTabTasks {
		return ""
	}
	rows, jobRows := m.sidebarTaskRowsAndJobs(m.sidebarLayout().contentWidth)
	if m.sidebarCursor < 0 || m.sidebarCursor >= len(jobRows) || rows[jobRows[m.sidebarCursor]].job == nil {
		return ""
	}
	return rows[jobRows[m.sidebarCursor]].job.ID
}

// sidebarFollowJob moves the Tasks cursor to the row of job id after the list
// changed, so the selection stays on the same job. A job can move down into
// the finished part, or a new job can start above it. An empty id, or a job
// that is no longer listed, leaves the cursor where it is.
func (m *TuiModel) sidebarFollowJob(id string) {
	if id == "" {
		return
	}
	layout := m.sidebarLayout()
	rows, jobRows := m.sidebarTaskRowsAndJobs(layout.contentWidth)
	for i, line := range jobRows {
		if rows[line].job != nil && rows[line].job.ID == id {
			m.sidebarCursor = i
			m.sidebarScrollToLine(line, layout.contentHeight)
			return
		}
	}
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
	// with no catalog price. Kept as data (tests pin the propagation);
	// since 2026-08-23 the UI renders a plain dollar figure instead of
	// decorating it with the old "+?" convention.
	rollupUnpriced bool
	// separatorBefore is true when a separator line sits above this row: it is
	// the first finished row of a sibling group that has active rows above it.
	separatorBefore bool
	// continues is true for a resumed job: it is shown under the finished job
	// it continues, with that job's description and a marker.
	continues bool
}

// buildSubagentTree walks jobs.Job.ParentID to build the Subagents tab's
// tree, generically to whatever depth the registry actually has (today: one
// level, since a child cannot itself spawn a subagent — see
// subagentDeniedTools — but nothing here assumes that depth). Within each
// sibling group the active children come first, newest start first. A
// finished child (done, failed or truncated) follows, most recent end first.
// A child counts as active when it is running, waiting for an answer, or has
// an active child itself. A separator sits between the two parts when both
// are non-empty. Dimming at render time is unchanged.
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
	return m.buildSubagentTreeFrom(m.sortedBackgroundJobs())
}

// buildSubagentTreeFrom is buildSubagentTree for the newest-first list all
// from sortedBackgroundJobs, so a caller that already sorted it does not sort it again.
func (m TuiModel) buildSubagentTreeFrom(all []jobs.Job) []subagentTreeRow {
	byParent := map[string][]jobs.Job{}
	descriptions := map[string]string{}
	for _, j := range all {
		if j.Kind != jobs.KindSubagent {
			continue
		}
		descriptions[j.ID] = j.Description
		// A resumed job is listed under the job it continues. Its real
		// ParentID stays as it is, so notices do not change. The cost rollup
		// walks this tree, so the cost of the resumed job also counts in the
		// job it continues and in that job's ancestors.
		parent := j.ParentID
		if old, ok := m.resumedFrom[j.ID]; ok {
			parent = old
		}
		byParent[parent] = append(byParent[parent], j)
	}
	active := subagentActiveSet(byParent)
	for parent, kids := range byParent {
		byParent[parent] = sortSubagentSiblings(kids, active)
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
		kids := byParent[parentID]
		for i, j := range kids {
			own := usage[j.ID]
			cost, unpriced := rollupJobCost(j.ID, byParent, usage)
			_, continues := m.resumedFrom[j.ID]
			if continues {
				// Show the name of the original agent, also for a job that
				// continues a resumed job: follow the chain back to its start.
				origin := m.resumedFrom[j.ID]
				for next, ok := m.resumedFrom[origin]; ok; next, ok = m.resumedFrom[origin] {
					origin = next
				}
				if desc, ok := descriptions[origin]; ok {
					j.Description = desc
				}
			}
			rows = append(rows, subagentTreeRow{
				depth:           depth,
				job:             j,
				ownTokens:       own.Usage.Input + own.Usage.Output,
				rollupUSD:       cost,
				rollupUnpriced:  unpriced,
				separatorBefore: i > 0 && !active[j.ID] && active[kids[i-1].ID],
				continues:       continues,
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
	// A sorted key list is not enough to preserve parent-first ordering: a
	// descendant key can sort before the missing ancestor that contains it
	// (for example, "A" can be a child of the orphan group "P"). Build the
	// reverse lookup so each orphan walk can first follow its chain to the
	// highest reachable group.
	parentByID := map[string]string{}
	for _, kids := range byParent {
		for _, j := range kids {
			parentByID[j.ID] = j.ParentID
		}
	}
	var walkOrphan func(parentID string, depth int, chain map[string]bool)
	walkOrphan = func(parentID string, depth int, chain map[string]bool) {
		if visited[parentID] {
			return
		}
		// A malformed ParentID cycle has no parent-first ordering. Break it
		// deterministically by letting the first key reached through the
		// sorted orphan list be the cycle's root.
		if chain[parentID] {
			walk(parentID, depth)
			return
		}
		chain[parentID] = true
		if ancestor, ok := parentByID[parentID]; ok {
			if _, hasGroup := byParent[ancestor]; hasGroup && !visited[ancestor] {
				walkOrphan(ancestor, depth, chain)
			}
		}
		delete(chain, parentID)
		walk(parentID, depth)
	}
	for _, parent := range orphanParents {
		if visited[parent] {
			continue
		}
		walkOrphan(parent, 1, map[string]bool{})
	}

	return rows
}

// subagentLive reports whether a subagent is still active. Done, failed and
// truncated are the finished states; every other status is active.
func subagentLive(status jobs.Status) bool {
	switch status {
	case jobs.StatusDone, jobs.StatusFailed, jobs.StatusTruncated:
		return false
	default:
		return true
	}
}

// subagentActiveSet returns, for every job in byParent, whether it is active:
// live itself, or with an active child. The walk is memoized, and visited
// guards a ParentID cycle, the same hazard buildSubagentTree's walk guards.
func subagentActiveSet(byParent map[string][]jobs.Job) map[string]bool {
	active := map[string]bool{}
	visited := map[string]bool{}
	var isActive func(j jobs.Job) bool
	isActive = func(j jobs.Job) bool {
		if v, ok := active[j.ID]; ok {
			return v
		}
		if visited[j.ID] {
			return false
		}
		visited[j.ID] = true
		v := subagentLive(j.Status)
		for _, kid := range byParent[j.ID] {
			if isActive(kid) {
				v = true
			}
		}
		active[j.ID] = v
		return v
	}
	for _, kids := range byParent {
		for _, j := range kids {
			isActive(j)
		}
	}
	return active
}

// sortSubagentSiblings puts the active siblings first, newest start first.
// The finished siblings follow, most recent end first. Ties fall back to the
// newer start and then to the ID, so the order is stable between calls.
func sortSubagentSiblings(kids []jobs.Job, active map[string]bool) []jobs.Job {
	sorted := append([]jobs.Job(nil), kids...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if active[a.ID] != active[b.ID] {
			return active[a.ID]
		}
		if !active[a.ID] && !a.FinishedAt.Equal(b.FinishedAt) {
			return a.FinishedAt.After(b.FinishedAt)
		}
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.After(b.StartedAt)
		}
		return a.ID > b.ID
	})
	return sorted
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
