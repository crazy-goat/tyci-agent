package display

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m TuiModel) renderHistorySearchView() string {
	popup := m.renderHistorySearchContent()

	placed := lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		popup,
		lipgloss.WithWhitespaceBackground(lipgloss.Color("235")),
		lipgloss.WithWhitespaceChars(" "),
	)
	return placed
}

func (m TuiModel) renderHistorySearchContent() string {
	var b strings.Builder

	// Popup dimensions
	popupWidth := m.width - 16
	if popupWidth < 40 {
		popupWidth = 40
	}
	if popupWidth > 100 {
		popupWidth = 100
	}
	maxPopupHeight := m.height - 12
	if maxPopupHeight < 10 {
		maxPopupHeight = 10
	}
	if maxPopupHeight > 30 {
		maxPopupHeight = 30
	}

	// Title
	title := " History Search (Ctrl+R) "
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("28")).
		Width(popupWidth - 2).
		Align(lipgloss.Center)
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n")

	// Filter input line
	filterPrefix := " Filter: "
	filterVal := m.historySearchFilter
	if filterVal == "" {
		filterVal = " " // empty but visible cursor
	}
	filterLine := filterPrefix + filterVal
	filterStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("15")).
		Background(lipgloss.Color("236")).
		Width(popupWidth - 2)
	b.WriteString(filterStyle.Render(filterLine))
	b.WriteString("\n")

	// Separator
	sep := strings.Repeat("─", popupWidth-2)
	sepStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Width(popupWidth - 2)
	b.WriteString(sepStyle.Render(sep))
	b.WriteString("\n")

	// Available lines for items
	availableLines := maxPopupHeight - 6 // title + filter + sep + items + sep2 + hint
	if availableLines < 1 {
		availableLines = 1
	}

	totalResults := len(m.historySearchResults)

	// Compute scroll window: ensure cursor is visible
	visibleStart := 0
	if totalResults > availableLines {
		visibleStart = m.historySearchCursor - availableLines/2
		if visibleStart < 0 {
			visibleStart = 0
		}
		if visibleStart+availableLines > totalResults {
			visibleStart = totalResults - availableLines
		}
	}

	// Styles
	selectedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("0")).
		Background(lipgloss.Color("45")).
		Width(popupWidth - 4)
	normalStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Width(popupWidth - 4)

	renderedLines := 0
	for i := visibleStart; i < totalResults && renderedLines < availableLines; i++ {
		entry := m.historySearchResults[i]
		// Truncate to fit popup width
		maxLen := popupWidth - 6 // prefix "▸ " + some padding
		if maxLen < 10 {
			maxLen = 10
		}
		if runes := []rune(entry); len(runes) > maxLen {
			entry = string(runes[:maxLen-3]) + "..."
		}
		if i == m.historySearchCursor {
			b.WriteString(selectedStyle.Render("▸ " + entry))
		} else {
			b.WriteString(normalStyle.Render("  " + entry))
		}
		b.WriteString("\n")
		renderedLines++
	}

	// Fill remaining lines to maintain popup height
	for renderedLines < availableLines {
		b.WriteString("\n")
		renderedLines++
	}

	// Separator before hint
	sep2 := strings.Repeat("─", popupWidth-2)
	b.WriteString(sepStyle.Render(sep2))
	b.WriteString("\n")

	// Hint at bottom
	if totalResults == 0 {
		b.WriteString(normalStyle.Render("  No matching history entries"))
	} else {
		hint := fmt.Sprintf("  %d result(s) — ↑↓/PgUp/PgDn navigate, Enter select, Ctrl+R next, Esc cancel", totalResults)
		hintStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Width(popupWidth - 2)
		b.WriteString(hintStyle.Render(hint))
	}

	// Wrap in a bordered box
	content := b.String()
	boxStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63"))
	return boxStyle.Render(content)
}
