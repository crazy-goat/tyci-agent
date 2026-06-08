package display

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m TuiModel) handleMouseMsg(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Shift {
		return m, nil
	}
	if msg.Button == tea.MouseButtonWheelUp {
		m.atBottom = false
		m.scrollLine += 3
		m.clampScroll()
		return m, nil
	}
	if msg.Button == tea.MouseButtonWheelDown {
		m.scrollLine -= 3
		if m.scrollLine < 0 {
			m.scrollLine = 0
			m.atBottom = true
		}
		return m, nil
	}
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		m.openToolModalAt(msg.Y)
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

func (m *TuiModel) openGenericToolModal(idx int) {
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
