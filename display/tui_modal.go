package display

import tea "github.com/charmbracelet/bubbletea"

func (m TuiModel) updateSubagentModal(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape:
			// Close modal on ESC — always (even if running).
			// The subagent keeps running in background; its output
			// goes to the inline tool block after modal closes.
			m.subagentModalActive = false
			m.subagentModalContent.Reset()
			m.subagentModalToolIdx = -1
			m.subagentModalDone = false
			// Restore scroll state from before modal opened
			m.atBottom = m.savedAtBottom
			m.scrollLine = m.savedScrollLine
			return m, nil

		case tea.KeyCtrlC:
			// Quit the whole program
			m.quitting = true
			return m, tea.Quit

		case tea.KeyEnter:
			// Close modal on Enter when done
			if m.subagentModalDone {
				m.subagentModalActive = false
				m.subagentModalContent.Reset()
				m.subagentModalToolIdx = -1
				m.subagentModalDone = false
				// Restore scroll state from before modal opened
				m.atBottom = m.savedAtBottom
				m.scrollLine = m.savedScrollLine
			}
			return m, nil

		case tea.KeyUp, tea.KeyCtrlUp:
			if m.subagentModalScroll < m.subagentModalMaxScroll() {
				m.subagentModalScroll++
			}
			return m, nil

		case tea.KeyDown, tea.KeyCtrlDown:
			if m.subagentModalScroll > 0 {
				m.subagentModalScroll--
			}
			return m, nil

		case tea.KeyPgUp:
			page := m.subagentModalPageSize()
			m.subagentModalScroll += page
			if m.subagentModalScroll > m.subagentModalMaxScroll() {
				m.subagentModalScroll = m.subagentModalMaxScroll()
			}
			return m, nil

		case tea.KeyPgDown:
			page := m.subagentModalPageSize()
			m.subagentModalScroll -= page
			if m.subagentModalScroll < 0 {
				m.subagentModalScroll = 0
			}
			return m, nil

		case tea.KeyHome:
			m.subagentModalScroll = m.subagentModalMaxScroll()
			return m, nil

		case tea.KeyEnd:
			m.subagentModalScroll = 0
			return m, nil
		}

	case tea.MouseMsg:
		return m, nil

	case tuiMsgBlock:
		// Forward block messages to the normal handler so streaming
		// (tool-progress, tool-end, error, done, reset) works while
		// the subagent modal is active.
		m.handleBlockMsg(msg)
		return m, nil
	}

	return m, nil
}

// openModelPicker activates the model picker popup.
