package display

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m TuiModel) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyTab:
		m.switchModel(1)
		return m, nil
	case tea.KeyShiftTab:
		m.switchModel(-1)
		return m, nil
	}

	if handled, model, cmd := m.handleGlobalKey(msg); handled {
		return model, cmd
	}
	if !m.reading {
		return m.handleKeyWhileBusy(msg)
	}

	switch msg.Type {
	case tea.KeyEscape:
		if m.selection.Active || m.selection.Candidate {
			return m.clearSelection(), nil
		}
		m.input.Reset()
		return m, nil
	case tea.KeyEnter:
		if msg.Alt {
			// Pre-set height so that repositionView inside the textarea's
			// Update already uses the correct viewport height. Without this,
			// repositionView runs with the old (smaller) height, decides the
			// new cursor line is out of view, scrolls down, and then
			// SetHeight in capInputHeight can't undo that scroll — the first
			// line of input disappears.
			newH := m.input.LineCount() + 1
			if newH < 1 {
				newH = 1
			} else if newH > 10 {
				newH = 10
			}
			m.input.SetHeight(newH)
			m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m.capInputHeight()
			return m, nil
		}
		line := strings.TrimSpace(m.input.Value())
		switch strings.ToLower(line) {
		case "/model":
			m.input.Reset()
			m.openModelPicker()
			return m, nil
		}
		return m.submit(), statusTickCmd()
	case tea.KeyCtrlN, tea.KeyCtrlJ:
		// Same pre-set height logic as Alt+Enter above.
		newH := m.input.LineCount() + 1
		if newH < 1 {
			newH = 1
		} else if newH > 10 {
			newH = 10
		}
		m.input.SetHeight(newH)
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.capInputHeight()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.capInputHeight()
	return m, cmd
}

func (m TuiModel) handleGlobalKey(msg tea.KeyMsg) (bool, tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyPgUp:
		m = m.clearSelection()
		m.atBottom = false
		m.scrollLine += max(1, m.visibleLines())
		m.clampScroll()
		return true, m, nil
	case tea.KeyPgDown:
		m = m.clearSelection()
		m.scrollLine -= max(1, m.visibleLines())
		if m.scrollLine < 0 {
			m.scrollLine = 0
			m.atBottom = true
		}
		return true, m, nil
	case tea.KeyCtrlUp:
		m = m.clearSelection()
		m.atBottom = false
		m.scrollLine++
		m.clampScroll()
		return true, m, nil
	case tea.KeyCtrlDown:
		m = m.clearSelection()
		m.scrollLine--
		if m.scrollLine < 0 {
			m.scrollLine = 0
			m.atBottom = true
		}
		return true, m, nil
	case tea.KeyUp:
		return true, m.historyOlder(), nil
	case tea.KeyDown:
		return true, m.historyNewer(), nil
	case tea.KeyHome:
		m = m.clearSelection()
		m.atBottom = false
		m.scrollLine = m.totalRenderedLines()
		return true, m, nil
	case tea.KeyEnd:
		m = m.clearSelection()
		m.atBottom = true
		m.scrollLine = 0
		return true, m, nil
	case tea.KeyCtrlC:
		m.quitting = true
		return true, m, tea.Quit
	case tea.KeyCtrlD:
		if m.input.Value() == "" {
			m.quitting = true
			return true, m, tea.Quit
		}
	}
	return false, m, nil
}

func (m TuiModel) handleKeyWhileBusy(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEscape {
		// Clear the pending-message queue in addition to cancelling the
		// current request (issue #88). The user almost always presses ESC
		// because they want to "stop and start over" — leaving queued
		// messages in place would trigger an unwanted follow-up on the
		// very next Enter.
		m.clearMessageQueue()
		select {
		case m.cancelCh <- struct{}{}:
		default:
		}
		return m, nil
	}
	// Enter submits the typed line to the pending-message queue
	// (issue #88). Without this branch, Enter would fall through to
	// the textarea below, which interprets Enter as a newline — so
	// the user would see a new line in the textarea and the message
	// would never be enqueued. The slash-command and Alt+Enter
	// branches mirror the idle handler so the keyboard semantics
	// stay consistent.
	switch msg.Type {
	case tea.KeyEnter:
		if msg.Alt {
			// Alt+Enter: insert a newline in the textarea.
			newH := m.input.LineCount() + 1
			if newH < 1 {
				newH = 1
			} else if newH > 10 {
				newH = 10
			}
			m.input.SetHeight(newH)
			m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m.capInputHeight()
			return m, nil
		}
		return m.submit(), nil
	case tea.KeyCtrlN, tea.KeyCtrlJ:
		// Ctrl+N / Ctrl+J: insert a newline in the textarea.
		newH := m.input.LineCount() + 1
		if newH < 1 {
			newH = 1
		} else if newH > 10 {
			newH = 10
		}
		m.input.SetHeight(newH)
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.capInputHeight()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.capInputHeight()
	return m, cmd
}

// clearMessageQueue drops all pending user messages: both the rendering
// snapshot and the shared channel. Called on ESC (with cancel) and on
// /new (with conversation reset). Issue #88 acceptance criteria #5 and #6.
func (m *TuiModel) clearMessageQueue() {
	if len(m.queueItems) == 0 && m.queue == nil {
		return
	}
	m.queueItems = nil
	m.invalidateTotalLines()
	if m.queue != nil {
		// Non-blocking drain. Safe to call on the bubbletea event loop.
		for {
			select {
			case <-m.queue:
			default:
				return
			}
		}
	}
}

func (m TuiModel) historyOlder() TuiModel {
	if len(m.inputHistory) == 0 {
		return m
	}
	if m.historyIdx == -1 {
		m.stashedInput = m.input.Value()
		m.historyIdx = len(m.inputHistory) - 1
	} else if m.historyIdx > 0 {
		m.historyIdx--
	}
	m.input.SetValue(m.inputHistory[m.historyIdx])
	m.input.SetCursor(len(m.inputHistory[m.historyIdx]))
	m.capInputHeight()
	return m
}

func (m TuiModel) historyNewer() TuiModel {
	if m.historyIdx == -1 || len(m.inputHistory) == 0 {
		return m
	}
	m.historyIdx++
	if m.historyIdx >= len(m.inputHistory) {
		m.historyIdx = -1
		m.input.SetValue(m.stashedInput)
		m.input.SetCursor(len(m.stashedInput))
		m.stashedInput = ""
	} else {
		m.input.SetValue(m.inputHistory[m.historyIdx])
		m.input.SetCursor(len(m.inputHistory[m.historyIdx]))
	}
	m.capInputHeight()
	return m
}
