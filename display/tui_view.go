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
	if !m.ready || m.quitting || m.todoModalActive || m.subagentModalActive || m.pickerActive || m.historySearchActive || m.resumePickerActive {
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

	// ── History search modal overlay mode ──
	if m.historySearchActive {
		return m.renderHistorySearchView()
	}

	// ── Resume picker overlay mode ──
	if m.resumePickerActive {
		return m.renderResumePickerView()
	}

	// ── Todo list modal overlay mode ──
	if m.todoModalActive {
		return m.renderTodoModalView()
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
	queueH := m.queuePanelHeight()
	msgHeight := max(1, m.visibleLines()-queueH)

	// Top status bar (cwd)
	b.WriteString(m.buildTopBar())
	b.WriteString("\n")

	// ── Message region (cached, issue #84) ──
	// The message region (transcript viewport + welcome placeholder) is the
	// expensive part of renderFrame(): it iterates visible blocks, wraps each
	// line, and builds the renderBuffer. During long tool execution the only
	// thing firing is the 250ms status tick — the message region is unchanged.
	// Cache it and reuse the string until something invalidates it.
	region := m.buildMessageRegionCached(msgHeight)
	b.WriteString(region)
	b.WriteString("\n")

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

	// Queue panel (issue #88): shows pending user messages submitted while
	// the agent is busy. Renders zero-height when the queue is empty, so the
	// layout is byte-identical to the pre-feature behavior in that case.
	b.WriteString(m.renderQueuePanel(m.width))

	b.WriteString(m.input.View())
	return b.String()
}

// buildMessageRegionCached returns the rendered message region, reusing the
// cached string when nothing has changed (issue #84). msgHeight is the number
// of terminal rows available for the message viewport (height minus top bar,
// status bar, queue panel, and input). The cached value is stored on the
// pointer-referenced messageRegionCache so it survives bubbletea's value copy
// on every Update.
func (m *TuiModel) buildMessageRegionCached(msgHeight int) string {
	if m.messageRegion == nil {
		m.messageRegion = &messageRegionCache{}
	}
	c := m.messageRegion
	hasContent := len(m.blocks) > 0
	// Check if the cache is still valid: not dirty, has been built, and all
	// state that affects the viewport (scroll, selection, width, content
	// presence) is unchanged since the last build.
	if !c.dirty && c.cached != "" &&
		c.scrollLine == m.scrollLine &&
		c.atBottom == m.atBottom &&
		c.selectionVersion == m.selectionVersion &&
		c.selectionActive == m.selection.Active &&
		c.selectionFlash == m.selectionFlash &&
		c.width == m.width &&
		c.hasContent == hasContent {
		return c.cached
	}
	region := m.buildMessageRegion(msgHeight)
	c.cached = region
	c.dirty = false
	c.scrollLine = m.scrollLine
	c.atBottom = m.atBottom
	c.selectionVersion = m.selectionVersion
	c.selectionActive = m.selection.Active
	c.selectionFlash = m.selectionFlash
	c.width = m.width
	c.hasContent = hasContent
	return region
}

// buildMessageRegion renders the message region: the welcome placeholder when
// there are no blocks, or the visible transcript viewport (flat render lines
// wrapped, selection-highlighted, padded to fill msgHeight rows). Also
// populates m.renderBuffer for selection hit-testing.
func (m *TuiModel) buildMessageRegion(msgHeight int) string {
	var b strings.Builder

	hasContent := len(m.blocks) > 0
	if !hasContent {
		w := max(10, m.width-2)
		msg := lipgloss.NewStyle().Width(w).Align(lipgloss.Center).
			Foreground(lipgloss.Color("240")).
			Render("tyci TUI\nType a message, Enter to send\nCtrl+C to quit\nTab/Shift+Tab: switch model\n/model: pick model\nClick tool block to expand/collapse\nDrag to select and copy text\nSet TYCI_TUI_MOUSE=0 for native terminal selection")
		b.WriteString(msg)
		b.WriteString("\n")
		msgHeight -= lipgloss.Height(msg)
		// Pad remaining rows with empty selectable lines so the region
		// fills the viewport exactly (selection hit-testing + painter diff
		// rely on a fixed-height region).
		m.renderBuffer = newRenderBuffer(max(0, msgHeight))
		for y := 0; y < msgHeight; y++ {
			b.WriteString(m.renderSelectableLine("", y))
			b.WriteString("\n")
			m.renderBuffer.Add("", "empty", -1, -1, y)
		}
		return strings.TrimSuffix(b.String(), "\n")
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

	// Trim the trailing newline — the caller (renderFrame) adds its own
	// separator newline between the region and the status bar.
	return strings.TrimSuffix(b.String(), "\n")
}

// renderModelPickerView renders just the model picker popup on a blank background.
