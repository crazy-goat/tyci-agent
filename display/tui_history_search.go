package display

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// openHistorySearch activates the Ctrl+R history search modal.
// The current textarea value is stashed so it can be restored on Esc.
func (m *TuiModel) openHistorySearch() {
	m.historySearchActive = true
	m.stashedSearchInput = m.input.Value()
	m.historySearchFilter = ""
	m.historySearchCursor = 0
	m.rebuildHistorySearchResults()
}

// closeHistorySearch deactivates the search modal without selecting anything.
// The textarea is restored to the value it had before Ctrl+R was pressed.
func (m *TuiModel) closeHistorySearch() {
	m.historySearchActive = false
	m.historySearchFilter = ""
	m.historySearchCursor = 0
	m.historySearchResults = nil
	m.input.SetValue(m.stashedSearchInput)
	m.input.SetCursor(len(m.stashedSearchInput))
	m.stashedSearchInput = ""
	m.capInputHeight()
}

// selectHistorySearchEntry inserts the selected history entry into the textarea
// and closes the modal. The prompt is NOT auto-submitted.
func (m *TuiModel) selectHistorySearchEntry(entry string) {
	m.historySearchActive = false
	m.historySearchFilter = ""
	m.historySearchCursor = 0
	m.historySearchResults = nil
	m.stashedSearchInput = ""
	m.input.SetValue(entry)
	m.input.SetCursor(len(entry))
	m.capInputHeight()
}

// rebuildHistorySearchResults filters inputHistory to entries whose lowercased
// text contains the lowercased filter substring. Results are ordered newest
// first (same as inputHistory iteration from the end).
func (m *TuiModel) rebuildHistorySearchResults() {
	if m.historySearchFilter == "" {
		// Show all history, newest first
		m.historySearchResults = make([]string, 0, len(m.inputHistory))
		for i := len(m.inputHistory) - 1; i >= 0; i-- {
			m.historySearchResults = append(m.historySearchResults, m.inputHistory[i])
		}
	} else {
		filter := strings.ToLower(m.historySearchFilter)
		m.historySearchResults = make([]string, 0, len(m.inputHistory))
		for i := len(m.inputHistory) - 1; i >= 0; i-- {
			if strings.Contains(strings.ToLower(m.inputHistory[i]), filter) {
				m.historySearchResults = append(m.historySearchResults, m.inputHistory[i])
			}
		}
	}
	// Clamp cursor
	if m.historySearchCursor >= len(m.historySearchResults) {
		m.historySearchCursor = max(0, len(m.historySearchResults)-1)
	}
}

// updateHistorySearch handles all events while the history search modal is open.
func (m TuiModel) updateHistorySearch(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape:
			m.closeHistorySearch()
			return m, nil

		case tea.KeyEnter:
			if len(m.historySearchResults) > 0 && m.historySearchCursor < len(m.historySearchResults) {
				m.selectHistorySearchEntry(m.historySearchResults[m.historySearchCursor])
			}
			return m, nil

		case tea.KeyUp:
			if m.historySearchCursor > 0 {
				m.historySearchCursor--
			}
			return m, nil

		case tea.KeyDown:
			if m.historySearchCursor < len(m.historySearchResults)-1 {
				m.historySearchCursor++
			}
			return m, nil

		case tea.KeyHome:
			m.historySearchCursor = 0
			return m, nil

		case tea.KeyEnd:
			if len(m.historySearchResults) > 0 {
				m.historySearchCursor = len(m.historySearchResults) - 1
			}
			return m, nil

		case tea.KeyPgUp:
			m.historySearchCursor -= 10
			if m.historySearchCursor < 0 {
				m.historySearchCursor = 0
			}
			return m, nil

		case tea.KeyPgDown:
			m.historySearchCursor += 10
			if m.historySearchCursor >= len(m.historySearchResults) {
				m.historySearchCursor = max(0, len(m.historySearchResults)-1)
			}
			return m, nil

		case tea.KeyBackspace:
			if len(m.historySearchFilter) > 0 {
				m.historySearchFilter = m.historySearchFilter[:len(m.historySearchFilter)-1]
				m.rebuildHistorySearchResults()
			}
			return m, nil

		case tea.KeySpace:
			m.historySearchFilter += " "
			m.rebuildHistorySearchResults()
			return m, nil

		case tea.KeyCtrlR:
			// Ctrl+R inside the modal cycles to the next match (bash behavior)
			if len(m.historySearchResults) > 0 {
				m.historySearchCursor++
				if m.historySearchCursor >= len(m.historySearchResults) {
					m.historySearchCursor = 0
				}
			}
			return m, nil

		case tea.KeyTab, tea.KeyShiftTab:
			// Swallow tab in search modal
			return m, nil

		default:
			if msg.Type == tea.KeyRunes {
				m.historySearchFilter += string(msg.Runes)
				m.rebuildHistorySearchResults()
			}
			return m, nil
		}

	case tea.MouseMsg:
		// Block mouse events; allow scroll wheel to navigate
		if msg.Button == tea.MouseButtonWheelUp {
			if m.historySearchCursor > 0 {
				m.historySearchCursor--
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			if m.historySearchCursor < len(m.historySearchResults)-1 {
				m.historySearchCursor++
			}
			return m, nil
		}
		return m, nil
	}

	return m, nil
}
