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
type sidebarLayoutT struct {
	width, height int
	left, top     int
	contentTop    int // first content row, in screen coordinates
	contentHeight int // rows available for the tab's own list/text
}

func (m TuiModel) sidebarLayout() sidebarLayoutT {
	width := m.width * 2 / 5
	if width < 36 {
		width = 36
	}
	if width > m.width-1 {
		width = m.width - 1
	}
	if width < 10 {
		width = 10
	}
	height := m.height
	left := m.width - width
	if left < 0 {
		left = 0
	}
	// Rows: title(1) + tabs(1) + separator(1) = 3 before content;
	// separator(1) + footer(1) + hint(1) = 3 after it.
	contentHeight := height - 6
	if contentHeight < 1 {
		contentHeight = 1
	}
	return sidebarLayoutT{
		width:         width,
		height:        height,
		left:          left,
		top:           0,
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
	innerWidth := layout.width - 2 // minus the panel's own left/right margin

	var b strings.Builder

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("60")).
		Width(innerWidth).
		Padding(0, 1)
	b.WriteString(titleStyle.Render("Sidebar"))
	b.WriteString("\n")

	b.WriteString(m.renderSidebarTabs(innerWidth))
	b.WriteString("\n")

	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Width(innerWidth)
	b.WriteString(sepStyle.Render(strings.Repeat("─", innerWidth)))
	b.WriteString("\n")

	lines := m.sidebarTabLines(innerWidth)
	shown := 0
	for _, l := range lines {
		if shown >= layout.contentHeight {
			break
		}
		b.WriteString(lipgloss.NewStyle().Width(innerWidth).MaxWidth(innerWidth).Render(l))
		b.WriteString("\n")
		shown++
	}
	for shown < layout.contentHeight {
		b.WriteString(strings.Repeat(" ", innerWidth))
		b.WriteString("\n")
		shown++
	}

	b.WriteString(sepStyle.Render(strings.Repeat("─", innerWidth)))
	b.WriteString("\n")

	footerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Width(innerWidth)
	b.WriteString(footerStyle.Render(truncateToWidth(m.sidebarFooter(), innerWidth)))
	b.WriteString("\n")
	b.WriteString(footerStyle.Render(truncateToWidth(m.sidebarHint(), innerWidth)))

	box := lipgloss.NewStyle().
		Width(layout.width).
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
		return "↑↓ select  Enter resume  Tab/←→ switch tab  Esc close"
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
