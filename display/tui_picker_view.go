package display

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m TuiModel) renderModelPickerView() string {
	popup := m.renderModelPickerContent()

	// Use Place to center the popup in the background
	placed := lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		popup,
		lipgloss.WithWhitespaceBackground(lipgloss.Color("235")),
		lipgloss.WithWhitespaceChars(" "),
	)
	return placed
}

// ─── Subagent modal ─────────────────────────────────────────────────────

// subagentModalMaxScroll returns the maximum scroll offset (lines from bottom).
func (m TuiModel) renderModelPickerContent() string {
	var b strings.Builder

	// Popup dimensions
	popupWidth := m.width - 8
	if popupWidth < 40 {
		popupWidth = 40
	}
	// Cap max height
	maxPopupHeight := m.height - 4
	if maxPopupHeight < 10 {
		maxPopupHeight = 10
	}

	// Title
	title := " Select Model (type to filter, Enter to select, f to fav, Esc to cancel) "
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("60")).
		Width(popupWidth - 2).
		Align(lipgloss.Center)
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n")

	// Filter input line - using a simulated input
	filterPrefix := " Filter: "
	filterVal := m.pickerFilter
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
	availableLines := maxPopupHeight - 5 // title + filter + sep + hint + bottom margin
	if availableLines < 1 {
		availableLines = 1
	}

	// Header style
	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("238")).
		Width(popupWidth - 2)
	// Selected item style
	selectedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("0")).
		Background(lipgloss.Color("45")).
		Width(popupWidth - 2)
	// Normal item style
	normalStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Width(popupWidth - 2)
	// Favorite indicator style
	favStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("214"))
	favSelectedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("208")).
		Background(lipgloss.Color("45"))

	// Build filtered items with scrolling
	modelIdx := 0
	totalModels := m.pickerModelCount()
	visibleStart := 0
	if totalModels > availableLines {
		visibleStart = m.pickerCursor - availableLines/2
		if visibleStart < 0 {
			visibleStart = 0
		}
		if visibleStart+availableLines > totalModels {
			visibleStart = totalModels - availableLines
		}
	}

	renderedModels := 0
	headerRendered := 0 // tracks total rendered lines (headers + items)

	for _, item := range m.pickerItems {
		if item.isHeader {
			if renderedModels >= visibleStart && renderedModels < visibleStart+availableLines {
				b.WriteString(headerStyle.Render("  " + item.label))
				b.WriteString("\n")
				headerRendered++
			}
			// Headers before visibleStart also count as rendered to push content up
			if renderedModels < visibleStart {
				// This header is before visible range; we need to account for its space
				// but we don't render it
			}
			continue
		}
		isSelected := modelIdx == m.pickerCursor
		isVisible := renderedModels >= visibleStart && renderedModels < visibleStart+availableLines

		if isVisible {
			isFav := m.favoriteSet[item.value]
			var favMark string
			if isFav {
				if isSelected {
					favMark = favSelectedStyle.Render(" ★")
				} else {
					favMark = favStyle.Render(" ★")
				}
			} else {
				if isSelected {
					favMark = lipgloss.NewStyle().Background(lipgloss.Color("45")).Render("  ")
				} else {
					favMark = "  "
				}
			}

			var line string
			if isSelected {
				line = selectedStyle.Render("▸ " + item.label)
			} else {
				line = normalStyle.Render("  " + item.label)
			}
			b.WriteString(favMark)
			b.WriteString(line)
			b.WriteString("\n")
			headerRendered++
		}
		modelIdx++
		renderedModels++
	}

	// Fill remaining lines to maintain popup height
	for headerRendered < availableLines {
		b.WriteString("\n")
		headerRendered++
	}

	// Hint at bottom
	if totalModels == 0 {
		b.WriteString(normalStyle.Render("  No matching models"))
		b.WriteString("\n")
	} else {
		hint := fmt.Sprintf("  %d model(s) — ↑/↓ navigate, Enter select, f toggle fav, Esc cancel", totalModels)
		hintStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Width(popupWidth - 2)
		b.WriteString(hintStyle.Render(hint))
		b.WriteString("\n")
	}

	// Wrap in a bordered box
	content := b.String()
	boxStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Width(popupWidth).
		MaxWidth(popupWidth)
	return boxStyle.Render(content)
}
