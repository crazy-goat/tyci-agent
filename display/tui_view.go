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
	if !m.ready || m.quitting || m.todoModalActive || m.subagentModalActive || m.pickerActive || m.historySearchActive || m.resumePickerActive || m.btwModalActive || m.btwListActive || m.jobsModalActive {
		return 0
	}
	return m.messageRegionHeight()
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

	// ── Background jobs modal overlay mode (Ctrl+B) ──
	if m.jobsModalActive {
		return m.renderJobsModalView()
	}

	// ── Subagent modal overlay mode ──
	if m.subagentModalActive {
		return m.renderSubagentModalView()
	}

	// ── /btw list popup mode ──
	if m.btwListActive {
		return m.renderBtwListView()
	}

	// ── /btw live/preview modal overlay mode ──
	if m.btwModalActive {
		return m.renderBtwModalView()
	}

	// ── Model picker popup mode ──
	if m.pickerActive {
		return m.renderModelPickerView()
	}

	var b strings.Builder
	msgHeight := m.messageRegionHeight()

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

	// Background jobs panel: shows async subagent jobs (see tools/subagent.go's
	// async mode and TUI.SetJobEventBus). Renders zero-height when there are
	// no background jobs, so the layout is unchanged for anyone who never
	// uses subagent(async: true).
	b.WriteString(m.renderJobsPanel(m.width))

	// Queue panel (issue #88): shows pending user messages submitted while
	// the agent is busy. Renders zero-height when the queue is empty, so the
	// layout is byte-identical to the pre-feature behavior in that case.
	b.WriteString(m.renderQueuePanel(m.width))

	// "@" file-path candidates sit directly above the input, and render as
	// nothing at all when the popup is closed.
	b.WriteString(m.renderFileComplete(m.width))

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
	// Height belongs in the cache key: the region is a fixed-height block of
	// rows, so a cached one built at a different height is the wrong shape.
	// It changes without any content change whenever a panel appears, the
	// input grows a line, or the terminal is resized vertically at the same
	// width — and reusing it then leaves the frame short or overflowing.
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
		c.msgHeight == msgHeight &&
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
	c.msgHeight = msgHeight
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
			Render("tyci TUI\nType a message, Enter to send\nCtrl+C to quit\nTab/Shift+Tab: switch model\n/model: pick model\n/btw <question>: side conversation without derailing this one\nClick tool block to expand/collapse\nDrag to select and copy text\nSet TYCI_TUI_MOUSE=0 for native terminal selection")
		b.WriteString(msg)
		b.WriteString("\n")
		msgHeight -= lipgloss.Height(msg)
		// Pad remaining rows with empty selectable lines so the region
		// fills the viewport exactly (selection hit-testing + painter diff
		// rely on a fixed-height region).
		*m.renderBuffer = newRenderBuffer(max(0, msgHeight))
		for y := 0; y < msgHeight; y++ {
			b.WriteString(m.renderSelectableLine("", y))
			b.WriteString("\n")
			m.renderBuffer.Add("", "empty", -1, -1, y)
		}
		return strings.TrimSuffix(b.String(), "\n")
	}

	// Exactly msgHeight rows, already wrapped, anchored and padded — see
	// buildViewportRows for why the anchoring cannot be done by slicing the
	// line list.
	rows := m.buildViewportRows(msgHeight)
	*m.renderBuffer = newRenderBuffer(msgHeight)

	for y, row := range rows {
		b.WriteString(m.renderSelectableLine(row.Text, y))
		b.WriteString("\n")
		m.renderBuffer.Add(row.Text, row.SourceKind, row.BlockIndex, row.SourceLine, y)
	}

	// Trim the trailing newline — the caller (renderFrame) adds its own
	// separator newline between the region and the status bar.
	return strings.TrimSuffix(b.String(), "\n")
}

// renderModelPickerView renders just the model picker popup on a blank background.
