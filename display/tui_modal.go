package display

import tea "github.com/charmbracelet/bubbletea"

func (m TuiModel) updateSubagentModal(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case selectionFlashDoneMsg:
		m.selectionFlash = false
		return m, nil

	case selectionAutoCopyMsg:
		if msg.version == m.selectionVersion && m.selection.Active {
			m = m.copySelection()
			return m, copyFeedbackCmd(m)
		}
		return m, nil

	case statusMessageClearMsg:
		if m.statusMessage == msg.message {
			m.statusMessage = ""
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape:
			if m.selection.Active || m.selection.Candidate {
				return m.clearSelection(), nil
			}
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
			m = m.clearSelection()
			if m.subagentModalScroll < m.subagentModalMaxScroll() {
				m.subagentModalScroll++
			}
			return m, nil

		case tea.KeyDown, tea.KeyCtrlDown:
			m = m.clearSelection()
			if m.subagentModalScroll > 0 {
				m.subagentModalScroll--
			}
			return m, nil

		case tea.KeyPgUp:
			m = m.clearSelection()
			page := m.subagentModalPageSize()
			m.subagentModalScroll += page
			if m.subagentModalScroll > m.subagentModalMaxScroll() {
				m.subagentModalScroll = m.subagentModalMaxScroll()
			}
			return m, nil

		case tea.KeyPgDown:
			m = m.clearSelection()
			page := m.subagentModalPageSize()
			m.subagentModalScroll -= page
			if m.subagentModalScroll < 0 {
				m.subagentModalScroll = 0
			}
			return m, nil

		case tea.KeyHome:
			m = m.clearSelection()
			m.subagentModalScroll = m.subagentModalMaxScroll()
			return m, nil

		case tea.KeyEnd:
			m = m.clearSelection()
			m.subagentModalScroll = 0
			return m, nil
		}

	case tea.MouseMsg:
		return m.handleMouseMsg(msg)

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
