package display

import (
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// statusTickInterval is the interval between status-bar repaints while a
// request is in flight. 250ms reduces idle CPU compared to the previous
// 100ms (issue #83) while still providing smooth elapsed-time updates with
// 0.1s precision.
const statusTickInterval = 250 * time.Millisecond

// statusTickCmd returns a command that ticks every statusTickInterval while
// a request is in flight, keeping the elapsed-time counter in the status bar
// live.
func statusTickCmd() tea.Cmd {
	return tea.Tick(statusTickInterval, func(time.Time) tea.Msg {
		return statusTickMsg{}
	})
}

func (m TuiModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m TuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.historySearchActive {
		return m.updateHistorySearch(msg)
	}
	if m.resumePickerActive {
		return m.updateResumePicker(msg)
	}
	if m.pickerActive {
		return m.updatePicker(msg)
	}
	if m.todoModalActive {
		return m.updateTodoModal(msg)
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
	case statusTickMsg:
		// Keep ticking while request is in flight; stop when idle.
		if !m.reading {
			return m, statusTickCmd()
		}
		return m, nil
	case statusMessageClearMsg:
		if m.statusMessage == msg.message {
			m.statusMessage = ""
		}
		return m, nil
	case tuiResumeRequestMsg:
		// /resume opened (typically bubbles in while reading=true). Make
		// sure no active model-picker is also active — the two popup
		// modes are mutually exclusive for the centered overlay.
		m.openResumePicker(msg.entries)
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
	list := m.favoriteModels
	if len(list) == 0 {
		list = m.models
	}
	if len(list) == 0 {
		return
	}
	// Include the current model in the cycle even if it isn't a favorite, so
	// Tab always starts from where you are — without persisting it as one.
	inList := false
	cur := 0
	for i, mm := range list {
		if mm == m.modelName {
			inList = true
			cur = i
			break
		}
	}
	if !inList {
		list = append([]string{m.modelName}, list...)
		cur = 0
	}
	m.favIdx = (cur + delta + len(list)) % len(list)
	newModel := list[m.favIdx]
	m.modelName = newModel
	if m.modelChanges != nil {
		select {
		case m.modelChanges <- newModel:
		default:
		}
	}
}
