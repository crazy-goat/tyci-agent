package display

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/tools"
)

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

// sidebarColumnWidth is the sidebar's "normal" on-screen footprint (border
// included) — the same panelWidth+1 formula sidebarLayout used to compute
// inline before the sidebar became a side-by-side column. Factored out so
// both sidebarLayout and mainColumnWidth derive from this one formula
// instead of two copies that could drift apart.
func (m TuiModel) sidebarColumnWidth() int {
	// panelWidth is the Width() style parameter passed to the box below —
	// content plus padding, NOT including the border column.
	panelWidth := m.width * 2 / 5
	if panelWidth < 36 {
		panelWidth = 36
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
// sidebarActive cleared — the "shadow model" used to render or dispatch
// through the main conversation's own width-keyed logic while the sidebar
// is open (see renderWidth's doc comment for why clearing sidebarActive
// here, not just narrowing width, is required). This is the ONLY place that
// should ever construct such a copy: a second, independently hand-rolled
// shadow model that narrows .width but leaves sidebarActive=true silently
// double-narrows every width-keyed read through it (F13) — and unlike
// renderFrame's transient shadow, one built for dispatching a message
// (e.g. a mouse click) can leave that double-narrowed wrap PERMANENTLY
// cached in m.blocks[idx].cachedLines, since getBlockLines writes through a
// slice that shares the real model's backing array.
func (m TuiModel) mainShadow() TuiModel {
	shadow := m
	shadow.width = m.mainColumnWidth()
	shadow.sidebarActive = false
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
	// separator(1) + footer(1) + hint(1) = 3 after it.
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
		title = "Sidebar (Right to focus)"
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
	lines := m.sidebarTabLines(contentWidth)
	scroll := m.sidebarVisibleScrollForLineCount(layout, len(lines))
	shown := 0
	for i := scroll; i < len(lines) && shown < layout.contentHeight; i++ {
		b.WriteString(lipgloss.NewStyle().Width(contentWidth).MaxWidth(contentWidth).Render(lines[i]))
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

	footerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Width(contentWidth)
	b.WriteString(footerStyle.Render(truncateToWidth(m.sidebarFooter(), contentWidth)))
	b.WriteString("\n")
	b.WriteString(footerStyle.Render(truncateToWidth(m.sidebarHint(), contentWidth)))

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
		Background(lipgloss.Color("235")).
		BorderStyle(lipgloss.NormalBorder()).
		BorderLeft(true).BorderRight(false).BorderTop(false).BorderBottom(false).
		BorderForeground(borderColor).
		Render(b.String())

	return box
}

// renderSidebarTabs renders the tab row, highlighting the active one.
// sidebarTabAtX (tui_sidebar.go) maps a click on this row back to a tab
// index, so it must stay in sync with how this splits width evenly.
func (m TuiModel) renderSidebarTabs(width int) string {
	cell := width / sidebarTabCount
	if cell < 1 {
		cell = 1
	}
	active := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("45")).Width(cell)
	inactive := lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Width(cell)
	var b strings.Builder
	for i, name := range sidebarTabNames {
		label := truncateToWidth(name, cell)
		if i == m.sidebarTab {
			b.WriteString(active.Render(label))
		} else {
			b.WriteString(inactive.Render(label))
		}
	}
	return truncateToWidth(b.String(), width)
}

// sidebarFooter is the keybinding hint line, tab-specific where an action
// beyond navigation exists, and focus-specific since Left/Right mean
// different things depending on whether the sidebar currently has the
// keyboard (m.sidebarFocused — see tui_sidebar.go's updateSidebar).
func (m TuiModel) sidebarFooter() string {
	if !m.sidebarFocused {
		return "Right: focus sidebar  Tab/Shift+Tab switch model  Esc close"
	}
	nav := "←→ switch tab (← at first/→ at last exits to conversation)"
	switch m.sidebarTab {
	case sidebarTabSessions:
		return "↑↓ browse  Enter: open resume picker  " + nav + "  Esc close"
	case sidebarTabTasks:
		return "↑↓ select  Enter view  r resume  " + nav + "  Esc close"
	default:
		return nav + "  Esc close"
	}
}

// sidebarHint is the second footer line: a standing note about the bounded,
// process-local nature of job history (TODO item 1's "known limit"), shown
// on the two tabs where it applies.
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
	case sidebarTabTasks:
		return m.renderSidebarTasks(width)
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

func (m TuiModel) renderSidebarTasks(width int) []string {
	rows := m.sidebarTaskRows(width)
	out := make([]string, 0, len(rows))
	cursorLine := -1
	if m.sidebarCursor >= 0 {
		jobRows := m.sidebarTaskJobRows(width)
		if m.sidebarCursor < len(jobRows) {
			cursorLine = jobRows[m.sidebarCursor]
		}
	}
	for i, row := range rows {
		if row.isHeading {
			out = append(out, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245")).Width(width).Render(row.line))
			continue
		}
		out = append(out, rowStyle(width, i == cursorLine).Render(truncateToWidth(row.line, width)))
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

func (m TuiModel) renderSidebarBash(width int) []string {
	list := m.sidebarBashJobs()
	if len(list) == 0 {
		return []string{"", "  No backgrounded bash commands this session."}
	}
	var out []string
	for i, j := range list {
		line := " " + formatJobLine(j, max(1, width-1))
		out = append(out, rowStyle(width, i == m.sidebarCursor).Render(truncateToWidth(line, width)))
	}
	return out
}

func (m TuiModel) renderSidebarLua(width int) []string {
	history := tools.LuaRunHistory()
	if len(history) == 0 {
		return []string{"", "  No Lua tool runs this session."}
	}
	var out []string
	for i := len(history) - 1; i >= 0; i-- {
		r := history[i]
		icon, color := "✓", lipgloss.Color("114")
		if !r.Success {
			icon, color = "✗", lipgloss.Color("203")
		}
		iconStyled := lipgloss.NewStyle().Foreground(color).Render(icon)
		line := fmt.Sprintf(" %s %-20s %6s ago  %s", iconStyled,
			truncateString(r.Name, 20), formatDurationShort(time.Since(r.StartedAt)), r.Duration.Round(time.Millisecond))
		out = append(out, truncateToWidth(line, width))
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

// renderSidebarSubagents renders the Subagents tree — see buildSubagentTree.
// waiting_answer rows render undimmed regardless of depth (they must never
// look like inert history); finished (done/failed/truncated) rows dim.
func (m TuiModel) renderSidebarSubagents(width int) []string {
	rows := m.buildSubagentTree()
	var out []string
	for i, row := range rows {
		out = append(out, rowStyle(width, i == m.sidebarCursor).Render(truncateToWidth(m.formatSubagentRow(row, width), width)))
	}
	return out
}

func (m TuiModel) formatSubagentRow(row subagentTreeRow, width int) string {
	indent := strings.Repeat("  ", row.depth)
	tokens := fmtTokens(row.ownTokens)
	cost := "$" + fmtUSD(row.rollupUSD)

	if row.isRoot {
		return fmt.Sprintf("%smain  %s tok  %s", indent, tokens, cost)
	}

	icon, color := jobStatusIcon(row.job.Status)
	iconStyled := lipgloss.NewStyle().Foreground(color).Render(icon)
	label := row.job.Description
	if row.job.Status == jobs.StatusWaitingAnswer && row.job.Question != "" {
		label = "asks: " + row.job.Question
	}
	avail := width - len(indent) - 4 /* icon+space */ - len(tokens) - len(cost) - 8
	if avail < 4 {
		avail = 4
	}
	label = truncateString(label, avail)

	line := fmt.Sprintf("%s%s %s  %s tok  %s", indent, iconStyled, label, tokens, cost)

	// Dim a finished row so a live one stands out — except waiting_answer,
	// which must never read as history (TODO item 1's explicit requirement).
	switch row.job.Status {
	case jobs.StatusDone, jobs.StatusFailed, jobs.StatusTruncated:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(line)
	default:
		return line
	}
}
