package display

import (
	tea "github.com/charmbracelet/bubbletea"
)

// TranscriptProvider returns (title, lines, ok) for jobID. ok is false
// when no transcript exists (still running or never stashed). Implemented
// in main (transcript_viewer.go) under resumableMu; display never imports
// connector.
type TranscriptProvider func(jobID string) (title string, lines []string, ok bool)

type tuiSetTranscriptProviderMsg struct {
	fn TranscriptProvider
}

// SetTranscriptProvider wires the viewer's data source. Called once from
// main (next to SetSessionLister), outside the bubbletea goroutine, so it
// goes through the same message-send pattern as every other model mutation.
func (t *TUI) SetTranscriptProvider(fn TranscriptProvider) {
	t.prog.Send(tuiSetTranscriptProviderMsg{fn: fn})
}

// openTranscriptViewer opens the read-only transcript viewer. Mirrors
// openJobsModal/closeJobsModal in saving/restoring the main scroll state.
func (m *TuiModel) openTranscriptViewer(title string, lines []string) {
	m.savedScrollLine = m.scrollLine
	m.savedAtBottom = m.atBottom
	m.transcriptViewerActive = true
	m.transcriptViewerTitle = title
	m.transcriptViewerLines = lines
	m.transcriptViewerScroll = 0
	m.selectionVersion++
	m.selection = SelectionState{}
	m.selectionFlash = false
}

func (m *TuiModel) closeTranscriptViewer() {
	m.transcriptViewerActive = false
	m.transcriptViewerTitle = ""
	m.transcriptViewerLines = nil
	m.transcriptViewerScroll = 0
	m.atBottom = m.savedAtBottom
	m.scrollLine = m.savedScrollLine
	m.selectionVersion++
	m.selection = SelectionState{}
	m.selectionFlash = false
}

func (m TuiModel) transcriptViewerMaxScroll() int {
	total := len(m.transcriptViewerLines)
	h := m.subagentModalLayout().contentHeight
	if total <= h {
		return 0
	}
	return total - h
}

func (m TuiModel) transcriptViewerPageSize() int {
	return m.subagentModalLayout().contentHeight
}

func (m TuiModel) updateTranscriptViewer(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.transcriptViewerScroll > m.transcriptViewerMaxScroll() {
			m.transcriptViewerScroll = m.transcriptViewerMaxScroll()
		}
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
			m.closeTranscriptViewer()
			return m, nil
		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit
		case tea.KeyEnter:
			// Read-only viewer: Enter always closes (like Esc). Symmetric
			// with subagent modal's done-gated close; this one is always done.
			m.closeTranscriptViewer()
			return m, nil
		case tea.KeyUp, tea.KeyCtrlUp:
			m = m.clearSelection()
			if m.transcriptViewerScroll < m.transcriptViewerMaxScroll() {
				m.transcriptViewerScroll++
			}
			return m, nil
		case tea.KeyDown, tea.KeyCtrlDown:
			m = m.clearSelection()
			if m.transcriptViewerScroll > 0 {
				m.transcriptViewerScroll--
			}
			return m, nil
		case tea.KeyPgUp:
			m = m.clearSelection()
			page := m.transcriptViewerPageSize()
			m.transcriptViewerScroll += page
			if m.transcriptViewerScroll > m.transcriptViewerMaxScroll() {
				m.transcriptViewerScroll = m.transcriptViewerMaxScroll()
			}
			return m, nil
		case tea.KeyPgDown:
			m = m.clearSelection()
			page := m.transcriptViewerPageSize()
			m.transcriptViewerScroll -= page
			if m.transcriptViewerScroll < 0 {
				m.transcriptViewerScroll = 0
			}
			return m, nil
		case tea.KeyHome:
			m = m.clearSelection()
			m.transcriptViewerScroll = m.transcriptViewerMaxScroll()
			return m, nil
		case tea.KeyEnd:
			m = m.clearSelection()
			m.transcriptViewerScroll = 0
			return m, nil
		}
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'y' {
			full := ""
			for i, l := range m.transcriptViewerLines {
				if i > 0 {
					full += "\n"
				}
				full += l
			}
			m = m.copyText("transcript", full, false)
			return m, copyFeedbackCmd(m)
		}
	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonWheelUp {
			m = m.clearSelection()
			if m.transcriptViewerScroll < m.transcriptViewerMaxScroll() {
				m.transcriptViewerScroll += 3
			}
			if m.transcriptViewerScroll > m.transcriptViewerMaxScroll() {
				m.transcriptViewerScroll = m.transcriptViewerMaxScroll()
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			m = m.clearSelection()
			m.transcriptViewerScroll -= 3
			if m.transcriptViewerScroll < 0 {
				m.transcriptViewerScroll = 0
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			layout := m.subagentModalLayout()
			inModal := msg.X >= layout.left && msg.X < layout.left+layout.popupWidth &&
				msg.Y >= layout.top && msg.Y < layout.top+layout.boxHeight
			if !inModal {
				m.closeTranscriptViewer()
				return m, nil
			}
		}
		if msg.Button == tea.MouseButtonLeft {
			layout := m.subagentModalLayout()
			inBody := msg.X >= layout.left && msg.X < layout.left+layout.popupWidth &&
				msg.Y >= layout.top && msg.Y < layout.top+layout.boxHeight
			if inBody {
				return m.handleModalMouseMsg(msg)
			}
		}
		return m, nil
	}
	return m, nil
}
