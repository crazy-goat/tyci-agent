package display

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// closeSubagentModal is the single source of truth for closing the subagent
// modal. ESC, Enter (when done), and outside-click all funnel through here.
// Closing clears view state only: the block keeps its output, so reopening a
// still-running subagent shows the stream continuing instead of an empty box.
func (m *TuiModel) closeSubagentModal() {
	m.subagentModalActive = false
	m.subagentModalBlockIdx = -1
	m.subagentModalScroll = 0
	m.subagentModalDone = false
	m.subagentModalTitle = ""
	m.subagentModalStaticText = ""
	// Restore scroll state from before modal opened
	m.atBottom = m.savedAtBottom
	m.scrollLine = m.savedScrollLine
	// Clear any pending selection so it doesn't bleed into the main view.
	m.selectionVersion++
	m.selection = SelectionState{}
	m.selectionFlash = false
}

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
			m.closeSubagentModal()
			return m, nil

		case tea.KeyCtrlC:
			// Quit the whole program
			m.quitting = true
			return m, tea.Quit

		case tea.KeyEnter:
			// Close modal on Enter when done
			if m.subagentModalDone {
				m.closeSubagentModal()
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

		// "y" (yank) — copy the entire modal buffer to clipboard.
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'y' {
			fullText := strings.TrimRight(m.subagentModalText(), "\n")
			m = m.copyText("modal", fullText, false)
			return m, copyFeedbackCmd(m)
		}

	case tea.MouseMsg:
		// Scroll wheel always works regardless of position.
		if msg.Button == tea.MouseButtonWheelUp {
			m = m.clearSelection()
			if m.subagentModalScroll < m.subagentModalMaxScroll() {
				m.subagentModalScroll += 3
			}
			if m.subagentModalScroll > m.subagentModalMaxScroll() {
				m.subagentModalScroll = m.subagentModalMaxScroll()
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			m = m.clearSelection()
			m.subagentModalScroll -= 3
			if m.subagentModalScroll < 0 {
				m.subagentModalScroll = 0
			}
			return m, nil
		}
		// Left press outside modal body → close it (same as ESC).
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			layout := m.subagentModalLayout()
			inModal := msg.X >= layout.left && msg.X < layout.left+layout.popupWidth &&
				msg.Y >= layout.top && msg.Y < layout.top+layout.boxHeight
			if !inModal {
				m.closeSubagentModal()
				return m, nil
			}
		}

		// Route left-button press/motion/release inside the modal body to the
		// selection handler so text can be selected and copied (issue #76).
		// Press outside the content area (title bar, border) is a no-op.
		// Press on the title bar is excluded from selection.
		if msg.Button == tea.MouseButtonLeft {
			layout := m.subagentModalLayout()
			inBody := msg.X >= layout.left && msg.X < layout.left+layout.popupWidth &&
				msg.Y >= layout.top && msg.Y < layout.top+layout.boxHeight
			if inBody {
				return m.handleModalMouseMsg(msg)
			}
		}

		// Block all other mouse events (non-left-button, outside clicks)
		// from leaking through to background tool blocks.
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
