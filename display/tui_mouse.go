package display

import (
	"strings"
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

	if msg.Button == tea.MouseButtonWheelUp {
		m = m.clearSelection()
		m.atBottom = false
		m.scrollLine += 3
		m.clampScroll()
		return m, nil
	}
	if msg.Button == tea.MouseButtonWheelDown {
		m = m.clearSelection()
		m.scrollLine -= 3
		if m.scrollLine < 0 {
			m.scrollLine = 0
			m.atBottom = true
		}
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
		m.openToolModalAt(y)
		return m, nil
	}
	return m, nil
}

func (m *TuiModel) openToolModalAt(y int) {
	if y < 0 || y >= m.visibleLines() {
		return
	}
	idx := m.blockAtVisibleLine(y)
	if idx < 0 || m.blocks[idx].kind != "tool" {
		return
	}
	m.savedScrollLine = m.scrollLine
	m.savedAtBottom = m.atBottom

	if m.blocks[idx].toolName == "subagent" && !m.subagentModalActive {
		m.subagentModalActive = true
		m.subagentModalScroll = 0
		for qi, bidx := range m.toolQueue {
			if bidx == idx {
				m.subagentModalToolIdx = qi
				break
			}
		}
		return
	}
	if m.blocks[idx].toolName != "subagent" && m.blocks[idx].toolState == "done" {
		m.openGenericToolModal(idx)
	}
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

func (m *TuiModel) openGenericToolModal(idx int) {
	if idx < 0 || idx >= len(m.blocks) {
		return
	}
	if m.blocks[idx].flushed {
		m.ensureBlockResident(idx)
	}
	m.subagentModalActive = true
	m.subagentModalContent.Reset()
	m.subagentModalScroll = 0
	m.subagentModalDone = true
	title := m.blocks[idx].toolName
	if m.blocks[idx].content != "" {
		firstLine := strings.SplitN(m.blocks[idx].content, "\n", 2)[0]
		if firstLine != "" {
			title = truncateString(firstLine, 80)
		}
	}
	m.subagentModalTitle = title
	content := m.blocks[idx].output
	if content == "" {
		content = m.blocks[idx].content
	}
	m.subagentModalContent.WriteString(content)
	m.subagentModalToolIdx = -1
}
