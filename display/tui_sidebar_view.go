package display

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/crazy-goat/tyci-agent/jobs"
)

// sidebarBackground is the background of every row of the sidebar box. A row
// that is styled must set it again after each reset, see fillWidth.
const sidebarBackground = lipgloss.Color("235")

// sidebarLayoutT is the sidebar's own layout shape — a full-height column
// docked to the right of the (narrower) main conversation column, unlike
// modalLayout's centered popups.
//
// The box is built as lipgloss.NewStyle().Width(panelWidth).Padding(0,
// 1).BorderLeft(true)…: lipgloss's box model treats Width as the
// content-plus-padding size and adds the border ON TOP of it, so the box's
// actual on-screen footprint is panelWidth+1 column wide, and the text
// content inside starts 2 columns past the box's own left edge (1 border +
// 1 left padding) with contentWidth = panelWidth-2 (minus left+right
// padding). All of width/left/contentLeft/contentWidth below are derived
// from that single panelWidth so the renderer and the mouse hit-testing in
// tui_sidebar.go can never compute two different geometries again — the
// bug an earlier review round caught (sidebarTabAtX assumed the tab row
// started at layout.left with no border/padding offset at all).
type sidebarLayoutT struct {
	// width/height are the box's actual on-screen footprint (border
	// included) — what "is this click inside the panel at all" tests
	// against.
	width, height int
	// left/top are the box's on-screen top-left corner.
	left, top int
	// contentLeft/contentWidth are the on-screen column range of the actual
	// rendered text (tab labels, list rows) — i.e. left/width shifted past
	// the border and left padding lipgloss adds. Both the renderer
	// (renderSidebarTabs, the content loop) and the hit-tester
	// (sidebarTabAtX, the row-click math) read these same two fields.
	contentLeft, contentWidth int
	contentTop                int // first content row, in screen coordinates
	contentHeight             int // rows available for the tab's own list/text
}

// Sidebar limits, in columns. sidebarMinPanel is the minimum of panelWidth
// (content plus padding, see sidebarColumnWidth). chatMinColumns is the
// minimum width of the main conversation column.
const (
	sidebarMinPanel = 36
	chatMinColumns  = 40
)

// sidebarWidthSaveDelay is how long the sidebar width must stay unchanged
// before it is saved. Each Shift+Left/Right press restarts the wait.
const sidebarWidthSaveDelay = 500 * time.Millisecond

// sidebarWidthSaveMsg fires sidebarWidthSaveDelay after a width key press. It
// saves only when seq is still the newest press (see resizeSidebar).
type sidebarWidthSaveMsg struct {
	seq int
}

// sidebarColumnWidth is the sidebar's on-screen footprint (border included).
// The content and padding width (panelWidth) is round(width * percent / 100),
// where percent is sidebarWidthPercent. Then the limits apply: panelWidth is
// at least sidebarMinPanel, and the chat keeps chatMinColumns. On a narrow
// terminal both limits cannot hold; the sidebar minimum wins, as before.
// Both sidebarLayout and mainColumnWidth derive from this one function.
func (m TuiModel) sidebarColumnWidth() int {
	percent := m.sidebarWidthPercent
	if percent <= 0 {
		percent = defaultSidebarWidthPercent
	}
	panelWidth := int(math.Round(float64(m.width) * percent / 100))
	// The +1 border column is on top of panelWidth, so the chat keeps
	// chatMinColumns when panelWidth+1 <= m.width-chatMinColumns.
	if panelWidth > m.width-1-chatMinColumns {
		panelWidth = m.width - 1 - chatMinColumns
	}
	if panelWidth < sidebarMinPanel {
		panelWidth = sidebarMinPanel
	}
	// Leave room for the +1 border column the box adds on top of panelWidth.
	if panelWidth > m.width-2 {
		panelWidth = m.width - 2
	}
	if panelWidth < 10 {
		panelWidth = 10
	}
	return panelWidth + 1 // + the left border column
}

// resizeSidebar changes the sidebar width by one column: delta +1 is wider,
// -1 is narrower. A press that would break a limit does nothing. The new width
// is saved as a percentage of the terminal width, rounded to three decimals.
// The save runs after sidebarWidthSaveDelay, through sidebarWidthSaveMsg.
func (m TuiModel) resizeSidebar(delta int) (TuiModel, tea.Cmd) {
	if m.width <= 0 {
		return m, nil
	}
	footprint := m.sidebarColumnWidth() + delta
	if footprint-1 < sidebarMinPanel || footprint > m.width-chatMinColumns {
		return m, nil
	}
	m.sidebarWidthPercent = roundPercent(float64(footprint-1) * 100 / float64(m.width))
	// The same relayout as a resize (handleResizeFlush), done here directly:
	// the resize flush message would be dropped while the sidebar is focused.
	m.input.SetWidth(max(10, m.mainColumnWidth()-2))
	m.capInputHeight()
	m.invalidateAllBlockLineCounts()
	m.clampScroll()
	if m.painter != nil {
		m.painter.repaint()
	}
	m.sidebarScroll = m.sidebarVisibleScroll(m.sidebarLayout())
	m.sidebarWidthSeq++
	seq := m.sidebarWidthSeq
	return m, tea.Tick(sidebarWidthSaveDelay, func(time.Time) tea.Msg {
		return sidebarWidthSaveMsg{seq: seq}
	})
}

// handleSidebarWidthSave runs when sidebarWidthSaveMsg arrives. Only the newest
// press saves, so a run of presses gives one write.
func (m TuiModel) handleSidebarWidthSave(msg sidebarWidthSaveMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.sidebarWidthSeq {
		return m, nil
	}
	percent := m.sidebarWidthPercent
	return m, func() tea.Msg {
		// A failed write must not break the key press, like the sidebar
		// visibility persister (persistSidebarVisible).
		_ = saveSidebarWidthPercent(percent)
		return nil
	}
}

// mainColumnWidth is the width the main conversation column renders at:
// the full terminal width when the sidebar is closed, or the terminal width
// minus the sidebar's footprint when it's open. This is the single source
// of truth for the main/sidebar split — sidebarLayout derives its own width
// as m.width - mainColumnWidth() rather than recomputing sidebarColumnWidth
// independently, so the two columns can never disagree about where the
// split falls.
//
// minMain/sidebarFloor handle a terminal too narrow to give both columns
// their normal size: first try shrinking the sidebar down to sidebarFloor
// to free up room for main's minimum; if the terminal is too narrow even
// for that (roughly sub-40 columns total), there's no split that keeps both
// sides usable, so main just gets whatever's left above zero rather than
// letting either column go negative. Both sides render squeezed in that
// case, which is an acceptable degraded state — nothing crashes.
func (m TuiModel) mainColumnWidth() int {
	if m.widthFinal {
		return m.width
	}
	if !m.sidebarActive {
		return m.width
	}
	const minMain = 20
	const sidebarFloor = 20
	if main := m.width - m.sidebarColumnWidth(); main >= minMain {
		return main
	}
	main := m.width - sidebarFloor
	if main >= minMain {
		return main
	}
	if main < 0 {
		main = 0
	}
	return main
}

// renderWidth is the width the transcript's block lines are wrapped,
// glamour-rendered and cached at: the full terminal width normally, the
// narrowed main column while the sidebar is open.
//
// Every cache writer (renderBlock, forceRenderDirtyBlocks, the scrollback
// flush/page-in paths) must use THIS, never raw m.width. The reason: with
// the sidebar open, the only thing ever on screen is the narrowed main
// column (renderFrame's sidebar branch renders through a shadow model whose
// width IS mainColumnWidth), so a block glamoured at the full width comes
// back too wide — and buildViewportRows' overlong-line safety net then
// re-wraps an already-rendered markdown table as plain text, shredding its
// box-drawing borders. That is exactly the "tables render broken while the
// sidebar is open, resizing or re-toggling fixes it" report: the re-toggle
// worked because invalidateAllBlockLineCounts forced a re-render, which now
// happened under the shadow and thus at the narrow width. Rendering at
// renderWidth from the start keeps the caches at whatever width is actually
// displayed; openSidebar/closeSidebar already invalidate on the transition,
// so the caches re-flow both ways.
//
// NOT idempotent under sidebarActive=true if called on a model whose .width
// is ALREADY the narrowed mainColumnWidth (F13): mainColumnWidth narrows
// again on top of an already-narrow width, since it has no way to tell "this
// width is already final" from "this is the real, full terminal width" —
// both just look like some m.width with m.sidebarActive set. Concretely,
// real width=120 narrows once to 71; feed 71 back in with sidebarActive
// still true and it narrows AGAIN to 34. mainShadow() (below) is the ONLY
// sanctioned way to build a model in that shape, and it clears
// sidebarActive as part of constructing it — every caller that needs a
// main-column-width copy of the model (renderFrame's side-by-side render in
// tui_view.go, routeSidebarMsg's main-column mouse dispatch in
// tui_sidebar.go) must go through it rather than hand-narrowing .width
// again, or this idempotence guarantee silently breaks for that one caller.
func (m TuiModel) renderWidth() int {
	if m.sidebarActive {
		return m.mainColumnWidth()
	}
	return m.width
}

// mainShadow returns a copy of m narrowed to mainColumnWidth() with
// sidebarActive cleared — the "shadow model" used to RENDER through the
// main conversation's own width-keyed logic while the sidebar is open (see
// renderWidth's doc comment for why clearing sidebarActive here, not just
// narrowing width, is required). Render-only is load-bearing: clearing
// sidebarActive is safe exactly because nothing on a pure render path
// (renderMainColumn and everything it calls) treats sidebarActive as
// anything but a width-narrowing switch.
//
// Do NOT use this to dispatch a message (a mouse click, a resize) — a
// dispatched message can run a handler that reads sidebarActive as real UI
// state rather than a width hint (e.g. openSidebar/closeSidebar deciding
// their own open/closed transition), and clearing it there makes an
// already-open sidebar look closed to that handler. Use dispatchShadow
// (below) for that case — found by review of an F13 fix that used this for
// both and broke the sidebar's own mouse-click-to-open handling (double-
// narrowed input width, clobbered saved scroll position, a full cache
// invalidation, all on a single click).
func (m TuiModel) mainShadow() TuiModel {
	shadow := m
	shadow.width = m.mainColumnWidth()
	shadow.sidebarActive = false
	return shadow
}

// dispatchShadow returns a copy of m narrowed to mainColumnWidth() with
// widthFinal set, WITHOUT touching sidebarActive — unlike mainShadow, safe
// to dispatch a message through (e.g. routeSidebarMsg's main-column mouse
// handling in tui_sidebar.go). .width is narrowed directly (not just an
// override mainColumnWidth() consults) because other code — statusRightHit,
// buildContextCost — reads .width directly rather than going through
// mainColumnWidth(); widthFinal is what stops mainColumnWidth()/
// renderWidth() from narrowing that already-narrow value a second time
// (F13), while sidebarActive stays true and accurate for any handler that
// reads it as real UI state rather than a width hint (e.g.
// openSidebar/closeSidebar deciding their own open/closed transition).
func (m TuiModel) dispatchShadow() TuiModel {
	shadow := m
	shadow.width = m.mainColumnWidth()
	shadow.widthFinal = true
	return shadow
}

func (m TuiModel) sidebarLayout() sidebarLayoutT {
	totalWidth := m.width - m.mainColumnWidth()
	if totalWidth < 1 {
		totalWidth = 1
	}
	left := m.mainColumnWidth()
	height := m.height

	panelWidth := totalWidth - 1 // content+padding, excluding the border column
	if panelWidth < 1 {
		panelWidth = 1
	}
	contentLeft := left + 2        // border(1) + left padding(1)
	contentWidth := panelWidth - 2 // minus left+right padding
	if contentWidth < 1 {
		contentWidth = 1
	}

	// Rows: title(1) + tabs(1) + separator(1) = 3 before content;
	// separator(1) + hint(1) + key line(1) = 3 after it.
	contentHeight := height - 6
	if contentHeight < 1 {
		contentHeight = 1
	}
	return sidebarLayoutT{
		width:         totalWidth,
		height:        height,
		left:          left,
		top:           0,
		contentLeft:   contentLeft,
		contentWidth:  contentWidth,
		contentTop:    3,
		contentHeight: contentHeight,
	}
}

// renderSidebarColumn renders the sidebar's own self-contained block —
// layout.width columns by layout.height rows, meant to sit at the right
// edge of a lipgloss.JoinHorizontal with the (separately, narrower-)
// rendered main column (see tui_view.go's renderFrame). Unlike the old
// full-screen overlay this no longer wraps itself in lipgloss.Place; the
// caller is responsible for the join.
//
// The title bar and border are styled differently depending on
// m.sidebarFocused, so it's visually obvious which side of the
// conversation<->sidebar focus split (tui_sidebar.go) currently owns the
// keyboard.
func (m TuiModel) renderSidebarColumn() string {
	layout := m.sidebarLayout()
	// panelWidth is the Width() style parameter the box below is built
	// with — see sidebarLayoutT's doc comment: layout.width is the box's
	// total on-screen footprint (border included), one column MORE than
	// this. contentWidth is what actually goes inside (content+padding
	// minus the padding itself).
	panelWidth := layout.width - 1
	contentWidth := layout.contentWidth

	var b strings.Builder

	titleBg := lipgloss.Color("60")
	titleFg := lipgloss.Color("252")
	title := "Sidebar"
	if !m.sidebarFocused {
		// Dimmed title reads as "open, but the conversation still has the
		// keyboard" — the same visual language buildSubagentTree's dimmed
		// finished-job rows use for "not what currently has your attention".
		titleBg = lipgloss.Color("238")
		titleFg = lipgloss.Color("245")
		title = "Sidebar (Shift+Tab to focus)"
	}
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(titleFg).
		Background(titleBg).
		Width(contentWidth).
		Padding(0, 1)
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n")

	b.WriteString(m.renderSidebarTabs(contentWidth))
	b.WriteString("\n")

	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Width(contentWidth)
	b.WriteString(sepStyle.Render(strings.Repeat("─", contentWidth)))
	b.WriteString("\n")

	// Scroll window: sidebarVisibleScroll (tui_sidebar.go) is the one shared
	// clamp — the mouse row-click handler calls the exact same function, so
	// the two can never compute a different offset for the same frame (see
	// its doc comment for the bug that fixed).
	lines := m.sidebarBodyLines(layout, contentWidth)
	shown := 0
	for i := 0; i < len(lines) && shown < layout.contentHeight; i++ {
		// Cut the line before styling it. Width() alone wraps a long line
		// onto a second row, which pushes the key line off the last row.
		b.WriteString(lipgloss.NewStyle().Width(contentWidth).Render(ansi.Truncate(lines[i], contentWidth, "…")))
		b.WriteString("\n")
		shown++
	}
	for shown < layout.contentHeight {
		b.WriteString(strings.Repeat(" ", contentWidth))
		b.WriteString("\n")
		shown++
	}

	b.WriteString(sepStyle.Render(strings.Repeat("─", contentWidth)))
	b.WriteString("\n")

	// The hint row comes first, so the key line is the last row on every
	// tab, also on tabs where the hint is empty. Both rows are padded before
	// they are styled, so the padding keeps the sidebar background (see fillWidth).
	footerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	b.WriteString(footerStyle.Render(fillWidth(m.sidebarHint(), contentWidth)))
	b.WriteString("\n")
	b.WriteString(footerStyle.Render(fillWidth(m.sidebarFooter(), contentWidth)))

	borderColor := lipgloss.Color("63")
	if m.sidebarFocused {
		// Brighter border when the sidebar owns the keyboard — matches the
		// active-tab highlight color (renderSidebarTabs' "45") so the two
		// "you are here" cues agree.
		borderColor = lipgloss.Color("45")
	}
	box := lipgloss.NewStyle().
		Width(panelWidth).
		Height(layout.height).
		Padding(0, 1).
		Background(sidebarBackground).
		BorderStyle(lipgloss.NormalBorder()).
		BorderLeft(true).BorderRight(false).BorderTop(false).BorderBottom(false).
		BorderForeground(borderColor).
		Render(b.String())

	return box
}

// renderSidebarTabs renders the tab row, highlighting the active one. Each
// tab is only as wide as its label; the row is cut at width and padded to
// width. sidebarTabAtX (tui_sidebar.go) maps a click on this row back to a
// tab index, so it must stay in sync with sidebarTabLabel.
func (m TuiModel) renderSidebarTabs(width int) string {
	active := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("45"))
	// Each label sets the sidebar background itself: its reset would clear the
	// background of the box, and the padding after the labels is styled too.
	inactive := lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Background(sidebarBackground)
	var b strings.Builder
	for i := range sidebarTabNames {
		if i == m.sidebarTab {
			b.WriteString(active.Render(sidebarTabLabel(i)))
		} else {
			b.WriteString(inactive.Render(sidebarTabLabel(i)))
		}
	}
	row := truncateToWidth(b.String(), width)
	pad := strings.Repeat(" ", max(0, width-lipgloss.Width(row)))
	return row + lipgloss.NewStyle().Background(sidebarBackground).Render(pad)
}

// sidebarFooter is the keybinding hint line, tab-specific where an action
// beyond navigation exists, and focus-specific since Left/Right mean
// different things depending on whether the sidebar currently has the
// keyboard (m.sidebarFocused — see tui_sidebar.go's updateSidebar).
func (m TuiModel) sidebarFooter() string {
	// The width keys come first on the line: it is cut at the sidebar width,
	// and the tab keys at the end are the ones that get lost. Unfocused,
	// only Shift+Left/Right works, so only that is named.
	if !m.sidebarFocused {
		return "Shift+Tab: focus  Shift+←/→ width"
	}
	width := "Shift+←/→ or </>: width  "
	nav := "←→ switch tab (← at first/→ at last exits to conversation)"
	switch m.sidebarTab {
	case sidebarTabSessions:
		return width + "↑↓ browse  Enter: open resume picker  " + nav + "  Esc close"
	case sidebarTabTasks:
		return width + "↑↓ select  Enter view  r resume  " + nav + "  Esc close"
	case sidebarTabRuns:
		return width + "↑↓ select  Enter expand/collapse  " + nav + "  Esc close"
	default:
		return width + nav + "  Esc close"
	}
}

// sidebarHint is the line above the key line (sidebarFooter): a standing note
// about the bounded, process-local nature of job history (TODO item 1's
// "known limit"), shown on the two tabs where it applies. It is empty on the
// other tabs.
func (m TuiModel) sidebarHint() string {
	switch m.sidebarTab {
	case sidebarTabTasks:
		return "History: this session only, last 50 entries per source"
	case sidebarTabSessions:
		if m.sessionLister == nil {
			return "Session list unavailable in this build"
		}
		return ""
	default:
		return ""
	}
}

// sidebarTabLines dispatches to the active tab's content lines.
func (m TuiModel) sidebarTabLines(width int) []string {
	switch m.sidebarTab {
	case sidebarTabTokens:
		return m.buildUsageDetail(width)
	case sidebarTabSessions:
		return m.renderSidebarSessions(width)
	case sidebarTabRuns:
		return m.renderSidebarRuns(width)
	default:
		return nil
	}
}

// rowStyle returns the style for row i given the current cursor —
// highlighted when selected, plain otherwise.
func rowStyle(width int, selected bool) lipgloss.Style {
	if selected {
		return lipgloss.NewStyle().Width(width).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("45"))
	}
	return lipgloss.NewStyle().Width(width)
}

// sidebarBodyLines returns the lines of the active tab from the scroll
// position on, cut to the window the sidebar shows (the caller stops at
// contentHeight). The Tasks tab styles only the rows of that window; the
// other tabs style every line.
func (m TuiModel) sidebarBodyLines(layout sidebarLayoutT, width int) []string {
	if m.sidebarTab == sidebarTabTasks {
		rows := m.sidebarTaskRows(width)
		scroll := m.sidebarVisibleScrollForLineCount(layout, len(rows))
		end := min(len(rows), scroll+layout.contentHeight)
		// The cursor index moves with the window; a cursor above it gives a negative index, which matches no row.
		return styleSidebarTaskRows(rows[scroll:end], m.sidebarTaskCursorLine(rows)-scroll, width)
	}
	lines := m.sidebarTabLines(width)
	return lines[m.sidebarVisibleScrollForLineCount(layout, len(lines)):]
}

// sidebarTaskCursorLine returns the index in rows of the selected job row, or -1.
func (m TuiModel) sidebarTaskCursorLine(rows []sidebarTaskRow) int {
	if m.sidebarCursor >= 0 {
		jobRows := sidebarJobRowIndices(rows)
		if m.sidebarCursor < len(jobRows) {
			return jobRows[m.sidebarCursor]
		}
	}
	return -1
}

// styleSidebarTaskRows turns built Tasks rows into the styled lines of the
// tab. cursorLine is the index of the selected row in rows, or -1.
func styleSidebarTaskRows(rows []sidebarTaskRow, cursorLine, width int) []string {
	out := make([]string, 0, len(rows))
	for i, row := range rows {
		if row.isHeading {
			// Pad before styling, so the padding keeps the sidebar background (see fillWidth).
			out = append(out, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245")).Render(fillWidth(row.line, width)))
			continue
		}
		line := row.line
		if i == cursorLine {
			// Inner styles (status icon) end with an ANSI reset that would clear
			// the highlight background mid-line, so drop them on the selected row.
			line = ansi.Strip(line)
		} else {
			// A full reset would also clear the panel background after the icon;
			// reset only the foreground instead.
			line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[39m")
		}
		out = append(out, rowStyle(width, i == cursorLine).Render(truncateToWidth(line, width)))
	}
	if len(out) == 0 {
		return []string{"", "  No tasks recorded this session."}
	}
	return out
}

func (m TuiModel) renderSidebarSessions(width int) []string {
	entries := m.sidebarSessionEntries()
	if m.sessionLister == nil {
		return []string{"", "  Sessions aren't wired up in this build."}
	}
	if m.sessionsLoadedAt.IsZero() {
		return []string{"", "  Loading sessions..."}
	}
	if len(entries) == 0 {
		return []string{"", "  No sessions recorded for this project yet."}
	}
	var out []string
	for i, e := range entries {
		date := formatResumeDate(e.ModTime)
		prompt := truncateResumePrompt(e.FirstPrompt, max(1, width-len(date)-3))
		line := fmt.Sprintf(" %s  %s", date, prompt)
		out = append(out, rowStyle(width, i == m.sidebarCursor).Render(truncateToWidth(line, width)))
	}
	return out
}

// formatDurationShort renders a duration as whole seconds while recent,
// otherwise as whole minutes.
func formatDurationShort(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// taskColumnGap is the number of spaces between the token and the cost column
// of a Tasks row.
const taskColumnGap = 2

// subagentTokens is the token column of a Tasks row, for example "148k tok".
func subagentTokens(row subagentTreeRow) string {
	return fmtTokens(row.ownTokens) + " tok"
}

// subagentCost is the cost column of a Tasks row, for example "$0.053".
func subagentCost(row subagentTreeRow) string {
	return "$" + fmtUSD(row.rollupUSD)
}

// subagentColumnWidths returns the widths of the token and the cost column: the
// widest value of each over all rows. Every row uses the same widths, so the
// columns line up.
func subagentColumnWidths(rows []subagentTreeRow) (tokW, costW int) {
	for _, row := range rows {
		tokW = max(tokW, lipgloss.Width(subagentTokens(row)))
		costW = max(costW, lipgloss.Width(subagentCost(row)))
	}
	return tokW, costW
}

// formatSubagentRow lays out a Tasks row: the label on the left, then the token
// and the cost columns, right-aligned in tokW and costW columns. When the row is
// too narrow, the label is cut first, then the token column, then the cost column.
func (m TuiModel) formatSubagentRow(row subagentTreeRow, width, tokW, costW int) string {
	indent := strings.Repeat("  ", row.depth)
	right := fitCells([]string{padLeft(subagentTokens(row), tokW), padLeft(subagentCost(row), costW)}, taskColumnGap, width)

	if row.isRoot {
		return lineWithRight(indent+"main", right, width)
	}

	icon, color := jobStatusIcon(row.job.Status)
	label := row.job.Description
	if row.job.Status == jobs.StatusWaitingAnswer && row.job.Question != "" {
		label = "asks: " + row.job.Question
	}
	if row.continues {
		label = "↻ " + label
	}
	// Cut the row while it is plain text, so no escape sequence is split. The
	// icon is colored afterwards. It is the first character after the indent.
	line := lineWithRight(indent+icon+" "+label, right, width)
	if strings.HasPrefix(line, indent+icon) {
		iconStyled := lipgloss.NewStyle().Foreground(color).Render(icon)
		line = indent + iconStyled + line[len(indent+icon):]
	}

	// Dim a finished row so a live one stands out — except waiting_answer,
	// which must never read as history (TODO item 1's explicit requirement).
	switch row.job.Status {
	case jobs.StatusDone, jobs.StatusFailed, jobs.StatusTruncated:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(line)
	default:
		return line
	}
}
