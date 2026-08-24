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
// the input box as normal). Ctrl+Right from the conversation "walks into"
// the sidebar's tabs; Ctrl+Left/Ctrl+Right walk back out to the conversation
// from any tab. Plain Left/Right are untouched and keep switching sidebar
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
	"github.com/decodo/tyci/tools"
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
	sidebarTabCount
)

var sidebarTabNames = [sidebarTabCount]string{
	sidebarTabTokens:   "Tokens",
	sidebarTabSessions: "Sessions",
	sidebarTabTasks:    "Tasks",
}

// Compatibility aliases for older package-local tests. They are not rendered
// as separate tabs and all map to the unified Tasks view.
const (
	sidebarTabBash      = sidebarTabTasks
	sidebarTabLua       = sidebarTabTasks
	sidebarTabSubagents = sidebarTabTasks
)

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
	if !wasActive {
		// Only an actual closed->open transition changes the effective
		// width the transcript wraps at (mainColumnWidth narrows). Calling
		// this again while already active (e.g. clicking the status bar's
		// context figure to jump to the Tokens tab while some other tab is
		// already open) would just re-wrap everything for no reason.
		m.invalidateAllBlockLineCounts()
	}
}

// closeSidebar closes the sidebar and restores scroll state. See
// openSidebar's doc comment for why the effective transcript width changing
// back to full-screen needs the same explicit block-line-cache invalidation.
func (m *TuiModel) closeSidebar() {
	m.sidebarActive = false
	m.sidebarFocused = false
	m.sidebarCursor = 0
	m.sidebarScroll = 0
	m.atBottom = m.savedAtBottom
	m.scrollLine = m.savedScrollLine
	m.selectionVersion++
	m.selection = SelectionState{}
	m.selectionFlash = false
	m.invalidateAllBlockLineCounts()
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
// scrollable listing. Tokens and Lua are the latter today — Lua's rows have
// no action (no live view, no resume), so a moving cursor over them would
// just be a highlight with nothing behind it (see sidebarRowCount).
func (m TuiModel) sidebarSelectable() bool {
	switch m.sidebarTab {
	case sidebarTabSessions, sidebarTabTasks:
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
}

// sidebarClampScrollToCursor keeps sidebarCursor within the visible window
// [sidebarScroll, sidebarScroll+contentHeight), adjusting sidebarScroll by
// the minimum needed — the same "scroll just enough to reveal the cursor"
// behavior list-style pickers elsewhere in this package use.
func (m *TuiModel) sidebarClampScrollToCursor(contentHeight int) {
	if contentHeight < 1 {
		contentHeight = 1
	}
	cursorLine := m.sidebarCursor
	if m.sidebarTab == sidebarTabTasks {
		jobRows := m.sidebarTaskJobRows(m.sidebarLayout().contentWidth)
		if m.sidebarCursor < 0 || m.sidebarCursor >= len(jobRows) {
			return
		}
		cursorLine = jobRows[m.sidebarCursor]
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
			jobCount := m.sidebarRowCount()
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
//     Ctrl+Right is claimed here (entering focus) and everything else falls
//     through to the normal keymap. Tab/ShiftTab are deliberately never
//     claimed here at all — handleKeyMsg's normal case for them
//     (switchModel) applies whether or not the sidebar is open.
//   - tuiMsgBlock (streamed text/tool-progress/tool-end/error/done blocks)
//     must keep reaching handleBlockMsg whether or not the sidebar has
//     focus: unfocused, it's handled=false here and falls through to
//     Update()'s normal tuiMsgBlock case; focused, it's forwarded to
//     updateSidebar below, whose own switch has a tuiMsgBlock case for
//     exactly this (mirroring tui_modal.go's identical carve-out for the
//     subagent modal). Without that case, focusing the sidebar (Ctrl+Right,
//     to browse Bash/Subagents while the agent keeps working — the whole
//     point of a side-by-side layout) would silently drop every block that
//     arrives in that window — worse than cosmetic, since "done" is what
//     flips m.reading back to true (tui_blocks.go), so a dropped done
//     could leave the input stuck non-reading indefinitely.
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
			mainM := m
			mainM.width = m.mainColumnWidth()
			mainM.sidebarFocused = false
			newModel, cmd := mainM.handleMouseMsg(msg)
			result := newModel.(TuiModel)
			result.width = m.width // restore the real (unnarrowed) width
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
		// Ctrl+Right walks focus "into" the sidebar from the conversation
		// side, landing on whichever tab was already selected (not reset to
		// 0) — see tui_sidebar.go's package doc comment. Plain Right is left
		// alone so it keeps moving the cursor through the prompt text (it
		// was previously hijacked for this, which made it impossible to move
		// the prompt cursor right while the sidebar was open).
		if msg.Type == tea.KeyCtrlRight {
			m.sidebarFocused = true
			return true, m, nil
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
		switch msg.Type {
		case tea.KeyEscape:
			m.closeSidebarPersisted()
			return m, nil

		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit

		case tea.KeyCtrlT:
			m.closeSidebarPersisted()
			return m, nil

		case tea.KeyTab:
			// Tab/ShiftTab are never sidebar tab-switchers: the terminal
			// itself treats Tab as a focus-cycle key, which is the root of
			// the problem this arrows-only design avoids. They keep doing
			// exactly what they do in the normal (non-sidebar) keymap —
			// switch model — regardless of sidebar focus.
			m.switchModel(1)
			return m, nil

		case tea.KeyShiftTab:
			m.switchModel(-1)
			return m, nil

		case tea.KeyCtrlLeft, tea.KeyCtrlRight:
			// Ctrl+Left/Ctrl+Right always walk focus back OUT to the
			// conversation, from any tab, regardless of position. Symmetric
			// with Ctrl+Right entering the sidebar (routeSidebarMsg).
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
			return m, nil

		case tea.KeyRight:
			// Symmetric exit at the rightmost tab.
			if m.sidebarTab == sidebarTabCount-1 {
				m.sidebarFocused = false
				return m, nil
			}
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
			if m.sidebarTab == sidebarTabTasks && string(msg.Runes) == "r" {
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
				m.closeSidebarPersisted()
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
					if m.sidebarTab == sidebarTabTasks {
						jobRows := m.sidebarTaskJobRows(m.sidebarLayout().contentWidth)
						line := row + m.sidebarVisibleScroll(layout)
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
					} else {
						m.sidebarCursor = row
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

	case tuiMsgBlock:
		// Forward streamed blocks to the normal handler so streaming
		// (text, tool-progress, tool-end, error, done, reset) keeps updating
		// the transcript while the sidebar has focus — mirroring
		// tui_modal.go's identical case for the subagent modal. Without
		// this, a block arriving while sidebarFocused is true (routed here
		// by routeSidebarMsg's default branch) was silently dropped, and
		// since "done" is what sets m.reading = true (tui_blocks.go), the
		// input box could stay stuck non-reading even after the user
		// exited sidebar focus.
		m.handleBlockMsg(msg)
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
	case sidebarTabTasks:
		rows := m.sidebarTaskRows(m.sidebarLayout().contentWidth)
		jobRows := m.sidebarTaskJobRows(m.sidebarLayout().contentWidth)
		if m.sidebarCursor >= 0 && m.sidebarCursor < len(jobRows) {
			if job := rows[jobRows[m.sidebarCursor]].job; job != nil {
				m.closeSidebar()
				m.openJobResultModal(*job)
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
	rows := m.sidebarTaskRows(m.sidebarLayout().contentWidth)
	jobRows := m.sidebarTaskJobRows(m.sidebarLayout().contentWidth)
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

// sidebarTaskRow is one rendered line in Tasks. Group headings are not
// selectable; job rows retain pointers to the existing job records.
type sidebarTaskRow struct {
	group     string
	line      string
	job       *jobs.Job
	isHeading bool
	subagent  bool
}

// sidebarTaskRows keeps the three source groups separate and stable. Jobs and
// Lua history are each already sorted newest-first at the point they are read.
func (m TuiModel) sidebarTaskRows(width int) []sidebarTaskRow {
	rows := []sidebarTaskRow{{group: "Subagents", line: "Subagents", isHeading: true}}
	for _, treeRow := range m.buildSubagentTree() {
		row := sidebarTaskRow{group: "Subagents", line: m.formatSubagentRow(treeRow, width)}
		if !treeRow.isRoot {
			job := treeRow.job
			row.job = &job
			row.subagent = true
		}
		rows = append(rows, row)
	}

	rows = append(rows, sidebarTaskRow{group: "Bash", line: "Bash", isHeading: true})
	for _, job := range m.sidebarBashJobs() {
		job := job
		rows = append(rows, sidebarTaskRow{group: "Bash", line: " " + formatJobLine(job, max(1, width-1)), job: &job})
	}

	history := tools.LuaRunHistory()
	rows = append(rows, sidebarTaskRow{group: "Lua", line: "Lua", isHeading: true})
	for i := len(history) - 1; i >= 0; i-- {
		r := history[i]
		icon := "✓"
		if !r.Success {
			icon = "✗"
		}
		rows = append(rows, sidebarTaskRow{group: "Lua", line: fmt.Sprintf(" %s %-20s %6s ago  %s", icon,
			truncateString(r.Name, 20), formatDurationShort(time.Since(r.StartedAt)), r.Duration.Round(time.Millisecond))})
	}
	return rows
}

func (m TuiModel) sidebarTaskJobRows(width int) []int {
	rows := m.sidebarTaskRows(width)
	indices := make([]int, 0)
	for i, row := range rows {
		if row.job != nil {
			indices = append(indices, i)
		}
	}
	return indices
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
	sorted := append([]jobs.Job(nil), kids...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].StartedAt.Equal(sorted[j].StartedAt) {
			return sorted[i].ID > sorted[j].ID
		}
		return sorted[i].StartedAt.After(sorted[j].StartedAt)
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
