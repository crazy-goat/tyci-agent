package display

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/tools"
)

// sidebarLayoutT is the sidebar's own layout shape — right-anchored and
// full-height, unlike modalLayout's centered popups.
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

func (m TuiModel) sidebarLayout() sidebarLayoutT {
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

	totalWidth := panelWidth + 1 // + the left border column
	left := m.width - totalWidth
	if left < 0 {
		left = 0
	}
	height := m.height

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

// renderSidebarView renders the sidebar as a right-anchored, full-height
// panel over a dimmed background — see tui_sidebar.go's package doc comment
// for why this is a full-screen overlay rather than a live side-by-side
// column.
func (m TuiModel) renderSidebarView() string {
	layout := m.sidebarLayout()
	// panelWidth is the Width() style parameter the box below is built
	// with — see sidebarLayoutT's doc comment: layout.width is the box's
	// total on-screen footprint (border included), one column MORE than
	// this. contentWidth is what actually goes inside (content+padding
	// minus the padding itself).
	panelWidth := layout.width - 1
	contentWidth := layout.contentWidth

	var b strings.Builder

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("60")).
		Width(contentWidth).
		Padding(0, 1)
	b.WriteString(titleStyle.Render("Sidebar"))
	b.WriteString("\n")

	b.WriteString(m.renderSidebarTabs(contentWidth))
	b.WriteString("\n")

	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Width(contentWidth)
	b.WriteString(sepStyle.Render(strings.Repeat("─", contentWidth)))
	b.WriteString("\n")

	// Scroll window: sidebarScroll is kept in [0, max(0, len(lines)-
	// contentHeight)] by sidebarClampScroll/sidebarScrollBy (tui_sidebar.go)
	// on every cursor move, tab switch, and wheel event — clamped again
	// here defensively in case of a resize since the last one of those.
	lines := m.sidebarTabLines(contentWidth)
	scroll := m.sidebarScroll
	if maxScroll := len(lines) - layout.contentHeight; scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
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

	box := lipgloss.NewStyle().
		Width(panelWidth).
		Height(layout.height).
		Padding(0, 1).
		Background(lipgloss.Color("235")).
		BorderStyle(lipgloss.NormalBorder()).
		BorderLeft(true).BorderRight(false).BorderTop(false).BorderBottom(false).
		BorderForeground(lipgloss.Color("63")).
		Render(b.String())

	return lipgloss.Place(
		m.width, m.height,
		lipgloss.Right, lipgloss.Top,
		box,
		lipgloss.WithWhitespaceBackground(lipgloss.Color("235")),
		lipgloss.WithWhitespaceChars(" "),
	)
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
// beyond navigation exists.
func (m TuiModel) sidebarFooter() string {
	switch m.sidebarTab {
	case sidebarTabSessions:
		return "↑↓ browse  Enter: open resume picker  Tab/←→ switch tab  Esc close"
	case sidebarTabBash, sidebarTabSubagents:
		hint := "↑↓ select  Enter view"
		if m.sidebarTab == sidebarTabSubagents {
			hint += "  r resume"
		}
		return hint + "  Tab/←→ switch tab  Esc close"
	default:
		return "Tab/←→ switch tab  Esc close"
	}
}

// sidebarHint is the second footer line: a standing note about the bounded,
// process-local nature of job history (TODO item 1's "known limit"), shown
// on the two tabs where it applies.
func (m TuiModel) sidebarHint() string {
	switch m.sidebarTab {
	case sidebarTabBash, sidebarTabSubagents:
		return "History: this session only, last 50 finished jobs (oldest evicted)"
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
	case sidebarTabBash:
		return m.renderSidebarBash(width)
	case sidebarTabLua:
		return m.renderSidebarLua(width)
	case sidebarTabSubagents:
		return m.renderSidebarSubagents(width)
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

// formatDurationShort renders a duration the way the jobs panel's "quiet
// Xs" note does: seconds while recent, otherwise minutes.
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
	if row.rollupUnpriced {
		cost += "+?"
	}

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
