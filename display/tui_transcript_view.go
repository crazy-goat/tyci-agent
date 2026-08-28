package display

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m TuiModel) renderTranscriptViewerView() string {
	layout := m.subagentModalLayout()
	popupWidth := layout.popupWidth
	contentHeight := layout.contentHeight

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("60")).
		Width(popupWidth - 2).
		Padding(0, 1)
	title := titleStyle.Render(fmt.Sprintf(" %s ", truncateString(m.transcriptViewerTitle, popupWidth-4)))

	allLines := m.transcriptViewerLines
	totalLines := len(allLines)
	if totalLines == 0 {
		allLines = []string{"(empty transcript)"}
		totalLines = 1
	}

	var visibleStart int
	if totalLines <= contentHeight {
		visibleStart = 0
	} else {
		visibleStart = totalLines - contentHeight - m.transcriptViewerScroll
		if visibleStart < 0 {
			visibleStart = 0
		}
	}
	visibleEnd := visibleStart + contentHeight
	if visibleEnd > totalLines {
		visibleEnd = totalLines
	}

	contentLines := make([]string, 0, contentHeight)
	*m.modalRenderBuffer = newRenderBuffer(contentHeight)
	for i := visibleStart; i < visibleEnd; i++ {
		line := allLines[i]
		if runes := []rune(line); len(runes) > popupWidth-4 {
			line = string(runes[:popupWidth-4])
		}
		y := layout.contentTop + len(contentLines)
		m.modalRenderBuffer.Add(line, "transcript", -1, i, y)
		contentLines = append(contentLines, m.renderSelectableLine(line, y))
	}
	for len(contentLines) < contentHeight {
		y := layout.contentTop + len(contentLines)
		m.modalRenderBuffer.Add("", "transcript-empty", -1, -1, y)
		contentLines = append(contentLines, m.renderSelectableLine("", y))
	}
	contentStr := strings.Join(contentLines, "\n")

	var footerText string
	if m.transcriptViewerScroll > 0 {
		pct := int(float64(m.transcriptViewerScroll) / float64(max(1, m.transcriptViewerMaxScroll())) * 100)
		footerText = fmt.Sprintf(" \u2191 scrolled %d%%  \u2191\u2193 scroll  PgUp/Dn  Home/End  y copy all  ESC/Enter close ", pct)
	} else if totalLines > contentHeight {
		footerText = fmt.Sprintf(" \u2193 %d more lines  \u2191\u2193 scroll  y copy all  ESC/Enter close ", totalLines-contentHeight)
	} else {
		footerText = " \u2191\u2193 scroll  y copy all  ESC/Enter close "
	}
	footerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Width(popupWidth - 2).
		Padding(0, 1)
	footer := footerStyle.Render(footerText)

	box := lipgloss.JoinVertical(lipgloss.Top, title, contentStr, footer)
	bordered := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Render(box)

	placed := lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		bordered,
		lipgloss.WithWhitespaceBackground(lipgloss.Color("235")),
		lipgloss.WithWhitespaceChars(" "),
	)
	if m.statusMessage != "" {
		statusStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("114")).
			Background(lipgloss.Color("235")).
			Bold(true).
			Padding(0, 1)
		statusLine := statusStyle.Render(m.statusMessage)
		lines := strings.Split(placed, "\n")
		if len(lines) > 1 {
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
