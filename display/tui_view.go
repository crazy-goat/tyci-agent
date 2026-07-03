package display

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m TuiModel) View() string {
	frame := m.renderFrame()
	// When the custom painter is active it drives the terminal directly; the
	// returned string is discarded by bubbletea's nil renderer. Painting here
	// (rather than on a ticker) means idle frames cost nothing and key presses
	// repaint instantly. See tui_painter.go.
	if m.painter != nil {
		m.painter.paintRegion(frame, m.width, m.height, m.paintScrollBottom())
	}
	return frame
}

// paintScrollBottom returns the height of the top-of-screen message region the
// painter may hardware-scroll (rows [0, N)). It's the message viewport in the
// normal transcript view, and 0 (disabled) for full-screen overlays and states
// that never scroll a stream, where a plain line diff is correct and cheaper.
func (m TuiModel) paintScrollBottom() int {
	if !m.ready || m.quitting || m.subagentModalActive || m.pickerActive {
		return 0
	}
	return m.visibleLines()
}

func (m TuiModel) renderFrame() string {
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
			Render("tyci TUI\nType a message, Enter to send\nCtrl+C to quit\nTab/Shift+Tab: switch model\n/model: pick model\nClick tool block to expand/collapse\nDrag to select and copy text\nSet TYCI_TUI_MOUSE=0 for native terminal selection")
		b.WriteString(msg)
		b.WriteString("\n")
		msgHeight--
	}

	// Build a flat, cached line list covering only the visible viewport.
	// Virtual viewport rendering avoids O(n) processing of all blocks.
	allLines := m.buildFlatRenderLines()
	m.renderBuffer = newRenderBuffer(msgHeight)

	rendered := 0
	for _, line := range allLines {
		if rendered >= msgHeight {
			break
		}
		if m.width > 0 && lipgloss.Width(line.Text) > m.width {
			wrapped := wrapText(line.Text, m.width, 0)
			for _, wl := range strings.Split(wrapped, "\n") {
				if rendered >= msgHeight {
					break
				}
				wl = strings.TrimSuffix(wl, clearLine)
				renderedLine := m.renderSelectableLine(wl, rendered)
				b.WriteString(renderedLine)
				b.WriteString("\n")
				m.renderBuffer.Add(wl, line.SourceKind, line.BlockIndex, line.SourceLine, rendered)
				rendered++
			}
		} else {
			renderedLine := m.renderSelectableLine(line.Text, rendered)
			b.WriteString(renderedLine)
			b.WriteString("\n")
			m.renderBuffer.Add(line.Text, line.SourceKind, line.BlockIndex, line.SourceLine, rendered)
			rendered++
		}
	}
	for rendered < msgHeight {
		renderedLine := m.renderSelectableLine("", rendered)
		b.WriteString(renderedLine)
		b.WriteString("\n")
		m.renderBuffer.Add("", "empty", -1, -1, rendered)
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
