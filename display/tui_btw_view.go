package display

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// btwModalMaxScroll and btwModalPageSize mirror subagentModalMaxScroll/
// subagentModalPageSize's line-counting math, but operate on whichever entry
// the /btw modal currently shows instead of a single fixed builder — a /btw
// modal can display any of several independent entries over its lifetime.
func (m TuiModel) btwModalMaxScroll() int {
	if m.btwModalEntry == nil {
		return 0
	}
	lines := strings.Split(m.btwModalEntry.content.String(), "\n")
	contentHeight := m.subagentModalLayout().contentHeight
	if len(lines) <= contentHeight {
		return 0
	}
	return len(lines) - contentHeight
}

func (m TuiModel) btwModalPageSize() int {
	return m.subagentModalLayout().contentHeight
}

// renderBtwModalView renders the /btw live/preview modal — same centered
// 90%x90% box as the subagent modal (subagentModalLayout depends only on
// m.width/m.height, so it's reused as-is).
func (m TuiModel) renderBtwModalView() string {
	entry := m.btwModalEntry
	if entry == nil {
		return ""
	}
	layout := m.subagentModalLayout()
	popupWidth := layout.popupWidth
	contentHeight := layout.contentHeight

	status := "⟳ running..."
	if entry.done {
		status = "✓ done"
		if entry.errMsg != "" {
			status = "✗ error"
		}
	}
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("60")).
		Width(popupWidth-2).
		Padding(0, 1)
	title := titleStyle.Render(fmt.Sprintf(" btw: %s — %s ", truncateString(entry.Question, 60), status))

	allLines := strings.Split(entry.content.String(), "\n")
	totalLines := len(allLines)

	var visibleStart int
	if totalLines <= contentHeight {
		visibleStart = 0
	} else {
		visibleStart = totalLines - contentHeight - m.btwModalScroll
		if visibleStart < 0 {
			visibleStart = 0
		}
	}
	visibleEnd := visibleStart + contentHeight
	if visibleEnd > totalLines {
		visibleEnd = totalLines
	}

	lineStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	contentLines := make([]string, 0, contentHeight)
	for i := visibleStart; i < visibleEnd; i++ {
		line := allLines[i]
		if runes := []rune(line); len(runes) > popupWidth-4 {
			line = string(runes[:popupWidth-4])
		}
		contentLines = append(contentLines, lineStyle.Render(line))
	}
	for len(contentLines) < contentHeight {
		contentLines = append(contentLines, "")
	}
	contentStr := strings.Join(contentLines, "\n")

	var footerText string
	if entry.done {
		footerText = " ↑↓ scroll  ESC/Enter close "
	} else {
		footerText = " running in background — ESC closes this view without stopping it  ↑↓ scroll "
	}
	footerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Width(popupWidth-2).
		Padding(0, 1)
	footer := footerStyle.Render(footerText)

	box := lipgloss.JoinVertical(lipgloss.Top, title, contentStr, footer)
	bordered := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Render(box)

	return lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		bordered,
		lipgloss.WithWhitespaceBackground(lipgloss.Color("235")),
		lipgloss.WithWhitespaceChars(" "),
	)
}

// renderBtwListView renders the bare "/btw" list popup: one row per entry
// recorded this session (newest first), showing status and a short preview
// of the accumulated output — the same layout family as the /resume popup.
func (m TuiModel) renderBtwListView() string {
	entries := m.btwListEntries()

	popupWidth := m.width - 16
	if popupWidth < 50 {
		popupWidth = 50
	}
	if popupWidth > 110 {
		popupWidth = 110
	}
	maxPopupHeight := m.height - 10
	if maxPopupHeight < 10 {
		maxPopupHeight = 10
	}
	if maxPopupHeight > 30 {
		maxPopupHeight = 30
	}

	var b strings.Builder

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("60")).
		Width(popupWidth - 2).
		Align(lipgloss.Center)
	b.WriteString(titleStyle.Render(" /btw side-conversations "))
	b.WriteString("\n")

	sep := strings.Repeat("─", popupWidth-2)
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Width(popupWidth - 2)
	b.WriteString(sepStyle.Render(sep))
	b.WriteString("\n")

	reservedRows := 5
	availableLines := maxPopupHeight - reservedRows
	if availableLines < 1 {
		availableLines = 1
	}

	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("45"))
	normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	total := len(entries)
	visibleStart := 0
	if total > availableLines {
		visibleStart = m.btwListCursor - availableLines/2
		if visibleStart < 0 {
			visibleStart = 0
		}
		if visibleStart+availableLines > total {
			visibleStart = total - availableLines
		}
	}

	innerWidth := popupWidth - 4
	statusWidth := 9
	previewWidth := innerWidth - statusWidth - 2

	rendered := 0
	for i := visibleStart; i < total && rendered < availableLines; i++ {
		entry := entries[i]
		isSelected := i == m.btwListCursor

		statusStr := "running"
		if entry.done {
			statusStr = "done"
			if entry.errMsg != "" {
				statusStr = "error"
			}
		}
		preview := btwPreview(entry, previewWidth)

		prefix := "  "
		if isSelected {
			prefix = "▸ "
		}
		row := fmt.Sprintf("%s%-*s  %s", prefix, statusWidth, statusStr, preview)
		if isSelected {
			b.WriteString(selectedStyle.Render(row))
		} else {
			b.WriteString(normalStyle.Render(row))
		}
		b.WriteString("\n")
		rendered++
	}
	for rendered < availableLines {
		b.WriteString("\n")
		rendered++
	}

	b.WriteString(sepStyle.Render(sep))
	b.WriteString("\n")

	var hint string
	if total == 0 {
		hint = "  No /btw side-conversations yet this session"
	} else {
		hint = fmt.Sprintf("  %d entr%s — ↑↓ navigate, Enter open, Esc cancel", total, pluralY(total))
	}
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Width(popupWidth - 2)
	b.WriteString(hintStyle.Render(hint))

	boxStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63"))
	popup := boxStyle.Render(b.String())

	return lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		popup,
		lipgloss.WithWhitespaceBackground(lipgloss.Color("235")),
		lipgloss.WithWhitespaceChars(" "),
	)
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// btwPreview builds the one-line "question — output preview" summary shown
// in the /btw list, collapsing newlines and truncating to maxWidth.
func btwPreview(entry *BtwEntry, maxWidth int) string {
	q := strings.Join(strings.Fields(entry.Question), " ")
	out := strings.Join(strings.Fields(entry.content.String()), " ")
	line := q
	if out != "" {
		line = q + " — " + out
	}
	if maxWidth <= 0 {
		return ""
	}
	runes := []rune(line)
	if len(runes) <= maxWidth {
		return line
	}
	if maxWidth <= 3 {
		return strings.Repeat(".", maxWidth)
	}
	return string(runes[:maxWidth-3]) + "..."
}
