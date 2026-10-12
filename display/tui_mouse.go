package display

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m TuiModel) handleMouseMsg(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Shift {
		return m, nil
	}

	// Top bar click (row 0): detect clicks on the todos counter.
	if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress && msg.Y == 0 {
		if m.topBarCounterHit(msg.X) == "todos" {
			m.openTodoModal()
			return m, nil
		}
		return m, nil
	}

	// Status bar click: the context figure (buildContextCost's right-hand
	// side) opens the sidebar on the Tokens tab. The status bar is always
	// exactly one row, directly below the topbar + fixed-height message
	// region — see messageRegionHeight's doc comment — so its Y is
	// deterministic even though nothing above it has a fixed height of its
	// own once jobs/queue panels are involved (those come AFTER the status
	// bar, not before).
	if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress && msg.Y == m.statusBarY() {
		if m.statusRightHit(msg.X) {
			m.openSidebar(sidebarTabTokens)
			m.persistSidebarVisible(true)
			return m, nil
		}
		return m, nil
	}

	if msg.Button == tea.MouseButtonWheelUp {
		m = m.clearSelection()
		m.atBottom = false
		m.scrollLine += 3
		m.clampScroll()
		return m, nil
	}
	if msg.Button == tea.MouseButtonWheelDown {
		m.scrollDown(3)
		return m, nil
	}

	// Convert terminal screen Y (0 = top bar) to message-area Y (0 = first message line).
	// The top bar occupies row 0, so screen row 1 is the first message line.
	// See https://github.com/crazy-goat/tyci-agent/issues/87
	adjY := msg.Y - 1

	if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress && m.transcriptY(adjY) {
		m.selectionVersion++
		x := screenXToSelectionX(m, msg.X)
		m.selection = SelectionState{Candidate: true, AnchorX: x, AnchorY: adjY, CursorX: x, CursorY: adjY, PressX: msg.X, PressY: adjY}
		return m, nil
	}
	if msg.Action == tea.MouseActionMotion && m.selection.Candidate {
		y := m.clampTranscriptY(adjY)
		x := screenXToSelectionX(m, msg.X)
		if y != m.selection.PressY || msg.X != m.selection.PressX || m.selection.Dragging {
			m.selectionVersion++
			m.selection.Active = true
			m.selection.Dragging = true
			m.selection.CursorX = x
			m.selection.CursorY = y
			version := m.selectionVersion
			return m, tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg { return selectionAutoCopyMsg{version: version} })
		}
		return m, nil
	}
	if msg.Action == tea.MouseActionMotion && m.selection.Active {
		y := m.clampTranscriptY(adjY)
		x := screenXToSelectionX(m, msg.X)
		if y != m.selection.CursorY || x != m.selection.CursorX {
			m.selectionVersion++
			m.selection.Dragging = true
			m.selection.CursorX = x
			m.selection.CursorY = y
			version := m.selectionVersion
			return m, tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg { return selectionAutoCopyMsg{version: version} })
		}
		return m, nil
	}
	if msg.Action == tea.MouseActionRelease && (m.selection.Candidate || m.selection.Active) {
		if m.selection.Dragging || m.selection.Active {
			m.selection.Active = true
			m.selection.Candidate = false
			m.selection.CursorX = screenXToSelectionX(m, msg.X)
			m.selection.CursorY = m.clampTranscriptY(adjY)
			m = m.copySelection()
			return m, copyFeedbackCmd(m)
		}
		y := m.selection.PressY
		m = m.clearSelection()
		m.toggleBlockAt(y)
		return m, nil
	}
	return m, nil
}

// toggleBlockAt handles a click on screen row y. A group header toggles the
// group. A tool or thinking line opens or folds its body in place. The click
// reads the block of the shown model, so it also works in an agent view.
func (m *TuiModel) toggleBlockAt(y int) {
	if y < 0 || y >= m.messageRegionHeight() {
		return
	}
	line, ok := m.visibleLine(y)
	if !ok {
		return
	}
	// The header of a group toggles the group. Its steps open one by one.
	shown := m.shownModel()
	if line.SourceKind == "group" {
		shown.toggleGroup(line.BlockIndex)
		return
	}
	idx := line.BlockIndex
	if idx < 0 || idx >= len(shown.blocks) || (shown.blocks[idx].kind != "tool" && shown.blocks[idx].kind != "thinking") {
		return
	}
	// Scroll position is kept on the main model. The lines of the body are
	// added below the header, so the scroll offset grows by the same count.
	// The header then stays on its row, also at the bottom, where the view
	// leaves the auto-follow mode. clampScroll restores it when the offset is 0.
	before := shown.totalRenderedLines()
	shown.toggleBlockExpanded(idx)
	m.scrollLine += shown.totalRenderedLines() - before
	if m.scrollLine > 0 {
		m.atBottom = false
	}
	m.clampScroll()
}

// toggleBlockExpanded opens or folds the body of a tool or thinking block and
// drops the block's cached lines, so the next render shows the new state.
func (m *TuiModel) toggleBlockExpanded(idx int) {
	b := &m.blocks[idx]
	b.expanded = !b.expanded
	b.cachedLines = nil
	b.cachedLineCount = 0
	m.invalidateTotalLines()
}

// handleModalMouseMsg handles mouse events inside the subagent modal body
// for text selection. It mirrors handleMouseMsg but adapted for modal context:
// no wheel handling (dealt with in updateSubagentModal), no tool-modal opening.
func (m TuiModel) handleModalMouseMsg(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Shift {
		return m, nil
	}

	// Left press inside modal content area → start selection candidate.
	if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
		if m.transcriptY(msg.Y) {
			m.selectionVersion++
			x := screenXToSelectionX(m, msg.X)
			m.selection = SelectionState{
				Candidate: true,
				AnchorX:   x, AnchorY: msg.Y,
				CursorX: x, CursorY: msg.Y,
				PressX: msg.X, PressY: msg.Y,
			}
		}
		return m, nil
	}

	// Motion while candidate → promote to active selection.
	if msg.Action == tea.MouseActionMotion && m.selection.Candidate {
		y := m.clampTranscriptY(msg.Y)
		x := screenXToSelectionX(m, msg.X)
		if y != m.selection.PressY || msg.X != m.selection.PressX || m.selection.Dragging {
			m.selectionVersion++
			m.selection.Active = true
			m.selection.Dragging = true
			m.selection.CursorX = x
			m.selection.CursorY = y
			version := m.selectionVersion
			return m, tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg {
				return selectionAutoCopyMsg{version: version}
			})
		}
		return m, nil
	}

	// Motion while already active → update selection extent.
	if msg.Action == tea.MouseActionMotion && m.selection.Active {
		y := m.clampTranscriptY(msg.Y)
		x := screenXToSelectionX(m, msg.X)
		if y != m.selection.CursorY || x != m.selection.CursorX {
			m.selectionVersion++
			m.selection.Dragging = true
			m.selection.CursorX = x
			m.selection.CursorY = y
			version := m.selectionVersion
			return m, tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg {
				return selectionAutoCopyMsg{version: version}
			})
		}
		return m, nil
	}

	// Release → commit selection (copy to clipboard).
	if msg.Action == tea.MouseActionRelease && (m.selection.Candidate || m.selection.Active) {
		if m.selection.Dragging || m.selection.Active {
			m.selection.Active = true
			m.selection.Candidate = false
			m.selection.CursorX = screenXToSelectionX(m, msg.X)
			m.selection.CursorY = m.clampTranscriptY(msg.Y)
			m = m.copySelection()
			return m, copyFeedbackCmd(m)
		}
		// Click without drag: just clear selection (no tool modal to open).
		m = m.clearSelection()
		return m, nil
	}

	return m, nil
}
