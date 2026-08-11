package display

import (
	"sort"
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

		case tea.KeyPgUp:
			m.pickerCursor -= 10
			if m.pickerCursor < 0 {
				m.pickerCursor = 0
			}
			return m, nil

		case tea.KeyPgDown:
			m.pickerCursor += 10
			if m.pickerCursor >= m.pickerModelCount() {
				m.pickerCursor = m.pickerModelCount() - 1
			}
			if m.pickerCursor < 0 {
				m.pickerCursor = 0
			}
			return m, nil

		case tea.KeyBackspace:
			if runes := []rune(m.pickerFilter); len(runes) > 0 {
				m.pickerFilter = string(runes[:len(runes)-1])
				m.rebuildPickerItems()
			}
			return m, nil

		case tea.KeySpace:
			m.pickerFilter += " "
			m.rebuildPickerItems()
			return m, nil

		case tea.KeyTab, tea.KeyShiftTab:
			// Ignore tab in picker mode
			return m, nil

		case tea.KeyCtrlF:
			// Ctrl+F toggles favorite on the selected model (only when filter is empty)
			if m.pickerFilter == "" {
				m.togglePickerFavorite()
			}
			return m, nil

		case tea.KeyCtrlD:
			// Ctrl+D sets the selected model as default (only when filter is empty)
			if m.pickerFilter == "" {
				m.setDefaultModel()
			}
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
		// Block all mouse events so clicks don't leak through to
		// background elements. Allow scroll wheel to navigate models.
		if msg.Button == tea.MouseButtonWheelUp {
			if m.pickerCursor > 0 {
				m.pickerCursor--
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			if m.pickerCursor < m.pickerModelCount()-1 {
				m.pickerCursor++
			}
			return m, nil
		}
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
	// Update favIdx in favorites list
	for i, mm := range m.favoriteModels {
		if mm == model {
			m.favIdx = i
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

// rebuildPickerItems builds the filtered picker items list.
// Layout (when filter is empty):
//  1. Default section (single defaultModel, if set)
//  2. Favorites section (favoriteModels in stored order)
//  3. All providers, sorted alphabetically, with models sorted within each.
//     Default and favorites models are excluded from provider sections
//     (they only appear in their dedicated sections at the top) so each
//     model is shown exactly once.
//
// When a filter is active, only the provider sections are shown (filtered),
// in alphabetical order. Default/Favorites sections are skipped while
// filtering so the user can type to find any model — including favorites.
func (m *TuiModel) rebuildPickerItems() {
	m.pickerItems = nil
	modelCount := 0
	filter := strings.ToLower(m.pickerFilter)

	// Tracks models already shown in Default/Favorites sections, so we can
	// skip them in provider sections (no duplication).
	used := make(map[string]bool, 2+len(m.favoriteModels))
	if m.defaultModel != "" {
		used[m.defaultModel] = true
	}
	for _, f := range m.favoriteModels {
		used[f] = true
	}

	addItem := func(header, label, value string) {
		if header != "" {
			m.pickerItems = append(m.pickerItems, pickerItem{isHeader: true, label: header})
			return
		}
		m.pickerItems = append(m.pickerItems, pickerItem{isHeader: false, label: label, value: value})
		modelCount++
	}

	if filter == "" {
		// Default section (single entry).
		if m.defaultModel != "" {
			addItem("Default", "", "")
			addItem("", m.defaultModel, m.defaultModel)
		}

		// Favorites section (in favoriteModels order).
		if len(m.favoriteModels) > 0 {
			addItem("Favorites", "", "")
			for _, f := range m.favoriteModels {
				addItem("", f, f)
			}
		}
	}

	// Providers, sorted alphabetically; models within each sorted.
	provs := make([]ProviderModels, len(m.allProviders))
	copy(provs, m.allProviders)
	sort.Slice(provs, func(i, j int) bool { return provs[i].Name < provs[j].Name })

	for _, prov := range provs {
		var matched []string
		for _, model := range prov.Models {
			label := prov.Name + "/" + model
			if (filter == "" && used[label]) || (filter != "" && !strings.Contains(strings.ToLower(label), filter)) {
				continue
			}
			matched = append(matched, label)
		}
		if len(matched) == 0 {
			continue
		}
		sort.Strings(matched)
		addItem(prov.Name, "", "")
		for _, label := range matched {
			addItem("", label, label)
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

// togglePickerFavorite toggles the favorite status of the currently selected model.
func (m *TuiModel) togglePickerFavorite() {
	model := m.pickerSelectedModel()
	if model == "" {
		return
	}
	if m.favoriteSet[model] {
		// Remove from favorites
		delete(m.favoriteSet, model)
		newFavs := m.favoriteModels[:0]
		for _, f := range m.favoriteModels {
			if f != model {
				newFavs = append(newFavs, f)
			}
		}
		m.favoriteModels = newFavs
		if m.onFavoriteToggled != nil {
			m.onFavoriteToggled(model, false)
		}
	} else {
		// Add to favorites
		m.favoriteSet[model] = true
		m.favoriteModels = append(m.favoriteModels, model)
		if m.onFavoriteToggled != nil {
			m.onFavoriteToggled(model, true)
		}
	}
}

// setDefaultModel sets the currently selected model as the default.
func (m *TuiModel) setDefaultModel() {
	model := m.pickerSelectedModel()
	if model == "" {
		return
	}
	m.defaultModel = model
	if m.onDefaultChanged != nil {
		m.onDefaultChanged(model)
	}
}

// ─── Scroll helpers ───────────────────────────────────────────────────────

// truncateString shortens a string to maxLen with "..." if needed.
