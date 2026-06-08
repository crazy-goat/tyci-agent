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
		m.input.Reset()
		return m, nil
	case tea.KeyEnter:
		if msg.Alt {
			m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m.capInputHeight()
			return m, nil
		}
		line := strings.TrimSpace(m.input.Value())
		if strings.EqualFold(line, "/model") {
			m.input.Reset()
			m.openModelPicker()
			return m, nil
		}
		return m.submit(), nil
	case tea.KeyCtrlN, tea.KeyCtrlJ:
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
		m.atBottom = false
		m.scrollLine += max(1, m.visibleLines())
		m.clampScroll()
		return true, m, nil
	case tea.KeyPgDown:
		m.scrollLine -= max(1, m.visibleLines())
		if m.scrollLine < 0 {
			m.scrollLine = 0
			m.atBottom = true
		}
		return true, m, nil
	case tea.KeyCtrlUp:
		m.atBottom = false
		m.scrollLine++
		m.clampScroll()
		return true, m, nil
	case tea.KeyCtrlDown:
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
		m.atBottom = false
		m.scrollLine = m.totalRenderedLines()
		return true, m, nil
	case tea.KeyEnd:
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
		select {
		case m.cancelCh <- struct{}{}:
		default:
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.capInputHeight()
	return m, cmd
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
