package display

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m TuiModel) subagentModalMaxScroll() int {
	content := m.subagentModalText()
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	popupHeight := int(float64(m.height) * 0.9)
	// Subtract title (2) + footer (2) + borders (2) = ~6 lines
	avail := popupHeight - 6
	if avail < 1 {
		avail = 1
	}
	if totalLines <= avail {
		return 0
	}
	return totalLines - avail
}

// subagentModalPageSize returns the number of lines per page scroll.
func (m TuiModel) subagentModalPageSize() int {
	popupHeight := int(float64(m.height) * 0.9)
	avail := popupHeight - 6
	if avail < 1 {
		avail = 1
	}
	return avail
}

// renderSubagentModalView renders the subagent live output as a centered modal (90% w/h).
func (m TuiModel) renderSubagentModalView() string {
	layout := m.subagentModalLayout()
	popupWidth := layout.popupWidth
	contentHeight := layout.contentHeight

	// Build title line
	status := "⟳ running..."
	if m.subagentModalDone {
		status = "✓ done"
	}
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("60")).
		Width(popupWidth-2).
		Padding(0, 1)
	title := titleStyle.Render(fmt.Sprintf(" %s — %s ", m.subagentModalTitle, status))

	// Build content with scroll
	allLines := strings.Split(m.subagentModalText(), "\n")
	totalLines := len(allLines)

	var visibleStart int
	if totalLines <= contentHeight {
		visibleStart = 0
	} else {
		visibleStart = totalLines - contentHeight - m.subagentModalScroll
		if visibleStart < 0 {
			visibleStart = 0
		}
	}
	visibleEnd := visibleStart + contentHeight
	if visibleEnd > totalLines {
		visibleEnd = totalLines
	}

	// Render visible lines
	lineStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	contentLines := make([]string, 0, contentHeight)
	*m.modalRenderBuffer = newRenderBuffer(contentHeight)

	for i := visibleStart; i < visibleEnd; i++ {
		line := allLines[i]
		// Truncate long lines (no "...", just cut)
		if runes := []rune(line); len(runes) > popupWidth-4 {
			line = string(runes[:popupWidth-4])
		}
		y := layout.contentTop + len(contentLines)
		renderedLine := lineStyle.Render(line)
		m.modalRenderBuffer.Add(renderedLine, "modal", -1, i, y)
		contentLines = append(contentLines, m.renderSelectableLine(renderedLine, y))
	}

	// Fill remaining empty lines
	for len(contentLines) < contentHeight {
		y := layout.contentTop + len(contentLines)
		m.modalRenderBuffer.Add("", "modal-empty", -1, -1, y)
		contentLines = append(contentLines, m.renderSelectableLine("", y))
	}
	contentStr := strings.Join(contentLines, "\n")

	// Build footer
	var footerText string
	if m.subagentModalScroll > 0 {
		pct := int(float64(m.subagentModalScroll) / float64(max(1, m.subagentModalMaxScroll())) * 100)
		footerText = fmt.Sprintf(" ↑ scrolled %d%%  ↑↓ scroll  PgUp/Dn  Home/End  y copy all  ESC/Enter close ", pct)
	} else if totalLines > contentHeight {
		footerText = fmt.Sprintf(" ↓ %d more lines  ↑↓ scroll  y copy all  ESC/Enter close ", totalLines-contentHeight)
	} else {
		footerText = " ↑↓ scroll  y copy all  ESC/Enter close "
	}
	footerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Width(popupWidth-2).
		Padding(0, 1)
	footer := footerStyle.Render(footerText)

	// Combine into a bordered box
	box := lipgloss.JoinVertical(lipgloss.Top,
		title,
		contentStr,
		footer,
	)

	bordered := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Render(box)

	// Place it centered
	placed := lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		bordered,
		lipgloss.WithWhitespaceBackground(lipgloss.Color("235")),
		lipgloss.WithWhitespaceChars(" "),
	)

	// Overlay status message (e.g. "copied modal") above the modal box.
	if m.statusMessage != "" {
		statusStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("114")).
			Background(lipgloss.Color("235")).
			Bold(true).
			Padding(0, 1)
		statusLine := statusStyle.Render(m.statusMessage)
		lines := strings.Split(placed, "\n")
		if len(lines) > 1 {
			// Place the status message on the second line (after the top dim border),
			// centered horizontally.
			statusW := lipgloss.Width(statusLine)
			padLeft := (m.width - statusW) / 2
			if padLeft < 0 {
				padLeft = 0
			}
			lines[1] = strings.Repeat(" ", padLeft) + statusLine
			if padLeft+statusW < m.width {
				remainder := lipgloss.NewStyle().
					Background(lipgloss.Color("235")).
					Render(strings.Repeat(" ", m.width-padLeft-statusW))
				lines[1] += remainder
			}
			placed = strings.Join(lines, "\n")
		}
	}

	return placed
}

// renderModelPickerContent renders the model picker content without outer positioning.
