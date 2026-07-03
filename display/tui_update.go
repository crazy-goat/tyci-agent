package display

import (
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

func (m TuiModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m TuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.pickerActive {
		return m.updatePicker(msg)
	}
	if m.subagentModalActive {
		return m.updateSubagentModal(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleResize(msg)
	case resizeFlushMsg:
		return m.handleResizeFlush()
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
		return m.handleKeyMsg(msg)
	case tuiMsgBlock:
		m.handleBlockMsg(msg)
		return m, nil
	case tea.MouseMsg:
		return m.handleMouseMsg(msg)
	}
	return m, nil
}

func (m TuiModel) handleResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	m.input.SetWidth(max(10, msg.Width-2))
	m.resizeWidth = msg.Width
	m.resizeHeight = msg.Height
	if !m.resizePending {
		m.resizePending = true
		return m, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
			return resizeFlushMsg{}
		})
	}
	return m, nil
}

func (m TuiModel) handleResizeFlush() (tea.Model, tea.Cmd) {
	if m.resizePending {
		m.resizePending = false
		if m.width != m.resizeWidth || m.height != m.resizeHeight {
			m.width = m.resizeWidth
			m.height = m.resizeHeight
			m.input.SetWidth(max(10, m.resizeWidth-2))
		}
		m.invalidateAllBlockLineCounts()
		m.clampScroll()
		if m.painter != nil {
			// Geometry changed; force a clear+full redraw so no stale cells
			// from the previous size linger.
			m.painter.repaint()
		}
	}
	return m, nil
}

func (m *TuiModel) switchModel(delta int) {
	if len(m.models) == 0 {
		return
	}
	m.modelIdx = (m.modelIdx + delta + len(m.models)) % len(m.models)
	newModel := m.models[m.modelIdx]
	m.modelName = newModel
	if m.modelChanges != nil {
		select {
		case m.modelChanges <- newModel:
		default:
		}
	}
}
