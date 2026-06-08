package display

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m TuiModel) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape:
			m.closeModelPicker()
			return m, nil

		case tea.KeyEnter:
			// Select the currently highlighted model
			selected := m.pickerSelectedModel()
			if selected != "" {
				m.selectModel(selected)
			}
			return m, nil

		case tea.KeyUp:
			if m.pickerCursor > 0 {
				m.pickerCursor--
			}
			return m, nil

		case tea.KeyDown:
			modelCount := m.pickerModelCount()
			if m.pickerCursor < modelCount-1 {
				m.pickerCursor++
			}
			return m, nil

		case tea.KeyHome:
			m.pickerCursor = 0
			return m, nil

		case tea.KeyEnd:
			m.pickerCursor = m.pickerModelCount() - 1
			if m.pickerCursor < 0 {
				m.pickerCursor = 0
			}
			return m, nil

		case tea.KeyBackspace:
			if len(m.pickerFilter) > 0 {
				m.pickerFilter = m.pickerFilter[:len(m.pickerFilter)-1]
				m.rebuildPickerItems()
			}
			return m, nil

		case tea.KeyTab, tea.KeyShiftTab:
			// Ignore tab in picker mode
			return m, nil

		default:
			// Add character to filter
			if msg.Type == tea.KeyRunes {
				m.pickerFilter += string(msg.Runes)
				m.rebuildPickerItems()
			}
			return m, nil
		}

	case tea.MouseMsg:
		return m, nil
	}

	return m, nil
}

// updateSubagentModal handles keyboard input when the subagent modal is active.
// It also forwards tuiMsgBlock messages to handleBlockMsg so streaming
// (tool-progress, tool-end, error, done, reset) continues to work.
func (m *TuiModel) openModelPicker() {
	m.pickerActive = true
	m.pickerFilter = ""
	m.pickerCursor = 0
	m.rebuildPickerItems()
}

// closeModelPicker deactivates the model picker popup without changing the model.
func (m *TuiModel) closeModelPicker() {
	m.pickerActive = false
	m.pickerFilter = ""
	m.pickerCursor = 0
	m.pickerItems = nil
}

// selectModel picks a model and closes the picker.
func (m *TuiModel) selectModel(model string) {
	m.modelName = model
	// Update modelIdx in flat list
	for i, mm := range m.models {
		if mm == model {
			m.modelIdx = i
			break
		}
	}
	if m.modelChanges != nil {
		select {
		case m.modelChanges <- model:
		default:
		}
	}
	m.closeModelPicker()
}

// rebuildPickerItems builds the filtered picker items list from allProviders.
func (m *TuiModel) rebuildPickerItems() {
	m.pickerItems = nil
	modelCount := 0
	filter := strings.ToLower(m.pickerFilter)

	for _, prov := range m.allProviders {
		// Collect matching models for this provider
		var matched []string
		for _, model := range prov.Models {
			label := prov.Name + "/" + model
			if filter == "" || strings.Contains(strings.ToLower(label), filter) {
				matched = append(matched, label)
			}
		}
		if len(matched) == 0 {
			continue
		}
		// Add header
		m.pickerItems = append(m.pickerItems, pickerItem{isHeader: true, label: prov.Name})
		for _, label := range matched {
			m.pickerItems = append(m.pickerItems, pickerItem{isHeader: false, label: label, value: label})
			modelCount++
		}
	}

	// Clamp cursor
	if m.pickerCursor >= modelCount && modelCount > 0 {
		m.pickerCursor = modelCount - 1
	} else if modelCount == 0 {
		m.pickerCursor = 0
	}
}

// pickerModelCount returns the number of model items (not headers) in the picker.
func (m *TuiModel) pickerModelCount() int {
	count := 0
	for _, item := range m.pickerItems {
		if !item.isHeader {
			count++
		}
	}
	return count
}

// pickerSelectedModel returns the currently selected model (full "provider/model").
func (m *TuiModel) pickerSelectedModel() string {
	idx := 0
	for _, item := range m.pickerItems {
		if item.isHeader {
			continue
		}
		if idx == m.pickerCursor {
			return item.value
		}
		idx++
	}
	return ""
}

// ─── Scroll helpers ───────────────────────────────────────────────────────

// truncateString shortens a string to maxLen with "..." if needed.
