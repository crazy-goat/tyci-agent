package display

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func copyFeedbackCmd(m TuiModel) tea.Cmd {
	var cmds []tea.Cmd
	if m.selectionFlash {
		cmds = append(cmds, tea.Tick(180*time.Millisecond, func(time.Time) tea.Msg {
			return selectionFlashDoneMsg{}
		}))
	}
	if m.statusMessage != "" {
		msg := m.statusMessage
		cmds = append(cmds, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
			return statusMessageClearMsg{message: msg}
		}))
	}
	return tea.Batch(cmds...)
}
