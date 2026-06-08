package display

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m TuiModel) View() string {
	if !m.ready {
		return ""
	}
	if m.quitting {
		return ""
	}

	// ── Subagent modal overlay mode ──
	if m.subagentModalActive {
		return m.renderSubagentModalView()
	}

	// ── Model picker popup mode ──
	if m.pickerActive {
		return m.renderModelPickerView()
	}

	var b strings.Builder
	msgHeight := m.visibleLines()

	// Welcome message
	hasContent := len(m.blocks) > 0
	if !hasContent {
		w := max(10, m.width-2)
		msg := lipgloss.NewStyle().Width(w).Align(lipgloss.Center).
			Foreground(lipgloss.Color("240")).
			Render("tyci-agent TUI\nType a message, Enter to send\nCtrl+C to quit\nTab/Shift+Tab: switch model\n/model: pick model\nClick tool block to expand/collapse\nShift+click/drag to select text")
		b.WriteString(msg)
		b.WriteString("\n")
		msgHeight--
	}

	// Build a flat, cached line list and then slice the exact visible window.
	// The previous virtual-scrolling path selected overlapping blocks but did
	// not trim lines when the viewport started in the middle of a long block.
	// At bottom during streaming that showed the start of the current response
	// instead of the newest tokens until a resize rebuilt the layout.
	var allLines []string
	for i := range m.blocks {
		lines := m.getBlockLines(i, false)
		if len(lines) == 0 {
			continue
		}
		allLines = append(allLines, lines...)
		if i+1 < len(m.blocks) && !(m.blocks[i+1].kind == "tool" && m.blocks[i].kind == "tool") {
			allLines = append(allLines, "")
		}
	}
	if len(allLines) > 0 && allLines[len(allLines)-1] == "" {
		allLines = allLines[:len(allLines)-1]
	}

	totalLines := len(allLines)
	m.cachedTotalLines = totalLines

	var startIdx int
	if totalLines > msgHeight {
		startIdx = totalLines - msgHeight - m.scrollLine
		if startIdx < 0 {
			startIdx = 0
		}
	}

	rendered := 0
	for i := startIdx; i < totalLines && rendered < msgHeight; i++ {
		line := allLines[i]
		if m.width > 0 && lipgloss.Width(line) > m.width {
			wrapped := wrapText(line, m.width, 0)
			for _, wl := range strings.Split(wrapped, "\n") {
				if rendered >= msgHeight {
					break
				}
				wl = strings.TrimSuffix(wl, clearLine)
				b.WriteString(wl)
				b.WriteString("\n")
				rendered++
			}
		} else {
			b.WriteString(line)
			b.WriteString("\n")
			rendered++
		}
	}
	for rendered < msgHeight {
		b.WriteString("\n")
		rendered++
	}

	// Status bar
	status := m.buildStatus()
	statusStyle := lipgloss.NewStyle().
		Width(m.width).MaxWidth(m.width).
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("250"))
	if status != "" {
		b.WriteString(statusStyle.Render(status))
	} else {
		b.WriteString(statusStyle.Render(""))
	}
	b.WriteString("\n")

	b.WriteString(m.input.View())
	return b.String()
}

// renderModelPickerView renders just the model picker popup on a blank background.
