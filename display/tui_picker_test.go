package display

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// helper: build a TuiModel with given providers, favorites and default for picker tests.
func newPickerTestModel(allProviders []ProviderModels, favorites []string, defaultModel string) TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, allProviders, nil, favorites, nil, defaultModel, nil)
	m.width = 120
	m.height = 40
	m.ready = true
	return m
}

var testProviders = []ProviderModels{
	{Name: "openai", Models: []string{"gpt-4o", "gpt-4o-mini"}},
	{Name: "anthropic", Models: []string{"claude-sonnet-4-20250514", "claude-haiku"}},
}

// ─── togglePickerFavorite ───────────────────────────────────────────────

func TestTogglePickerFavorite_AddsModel(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.openModelPicker()
	// cursor starts at 0 => first model = openai/gpt-4o
	m.togglePickerFavorite()

	if !m.favoriteSet["openai/gpt-4o"] {
		t.Fatal("expected openai/gpt-4o in favoriteSet")
	}
	if len(m.favoriteModels) != 1 || m.favoriteModels[0] != "openai/gpt-4o" {
		t.Fatalf("favoriteModels = %v, want [openai/gpt-4o]", m.favoriteModels)
	}
}

func TestTogglePickerFavorite_RemovesModel(t *testing.T) {
	m := newPickerTestModel(testProviders, []string{"openai/gpt-4o"}, "")
	m.openModelPicker()
	m.togglePickerFavorite() // cursor at 0 => openai/gpt-4o => remove

	if m.favoriteSet["openai/gpt-4o"] {
		t.Fatal("expected openai/gpt-4o removed from favoriteSet")
	}
	if len(m.favoriteModels) != 0 {
		t.Fatalf("favoriteModels = %v, want []", m.favoriteModels)
	}
}

func TestTogglePickerFavorite_CallbackCalled(t *testing.T) {
	var called []string
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, testProviders, nil,
		nil, func(favs []string) { called = append([]string{}, favs...) }, "", nil)
	m.width = 120
	m.height = 40
	m.openModelPicker()

	m.togglePickerFavorite()
	if len(called) != 1 || called[0] != "openai/gpt-4o" {
		t.Fatalf("callback got %v, want [openai/gpt-4o]", called)
	}
}

func TestTogglePickerFavorite_ToggleSecondModel(t *testing.T) {
	m := newPickerTestModel(testProviders, []string{"openai/gpt-4o"}, "")
	m.openModelPicker()
	m.pickerCursor = 1 // openai/gpt-4o-mini
	m.togglePickerFavorite()

	if len(m.favoriteModels) != 2 {
		t.Fatalf("expected 2 favorites, got %d: %v", len(m.favoriteModels), m.favoriteModels)
	}
	if !m.favoriteSet["openai/gpt-4o-mini"] {
		t.Fatal("expected openai/gpt-4o-mini in favoriteSet")
	}
}

// ─── setDefaultModel ───────────────────────────────────────────────────

func TestSetDefaultModel_SetsModel(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.openModelPicker()
	m.setDefaultModel() // cursor at 0 => openai/gpt-4o

	if m.defaultModel != "openai/gpt-4o" {
		t.Fatalf("defaultModel = %q, want openai/gpt-4o", m.defaultModel)
	}
}

func TestSetDefaultModel_ReplacesPrevious(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "openai/gpt-4o")
	m.openModelPicker()
	m.pickerCursor = 2 // anthropic/claude-sonnet-4-20250514
	m.setDefaultModel()

	if m.defaultModel != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("defaultModel = %q, want anthropic/claude-sonnet-4-20250514", m.defaultModel)
	}
}

func TestSetDefaultModel_CallbackCalled(t *testing.T) {
	var got string
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, testProviders, nil,
		nil, nil, "", func(s string) { got = s })
	m.width = 120
	m.height = 40
	m.openModelPicker()

	m.setDefaultModel()
	if got != "openai/gpt-4o" {
		t.Fatalf("callback got %q, want openai/gpt-4o", got)
	}
}

func TestSetDefaultModel_NoopWhenNoModels(t *testing.T) {
	m := newPickerTestModel(nil, nil, "")
	m.openModelPicker()
	m.setDefaultModel()

	if m.defaultModel != "" {
		t.Fatalf("defaultModel = %q, want empty", m.defaultModel)
	}
}

// ─── switchModel (Tab/Shift+Tab) ───────────────────────────────────────

func TestSwitchModel_CyclesFavorites(t *testing.T) {
	m := newPickerTestModel(testProviders, []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-20250514"}, "")
	m.modelName = "openai/gpt-4o"
	m.favIdx = 0

	m.switchModel(1)
	if m.modelName != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("modelName = %q, want anthropic/claude-sonnet-4-20250514", m.modelName)
	}
	if m.favIdx != 1 {
		t.Fatalf("favIdx = %d, want 1", m.favIdx)
	}
}

func TestSwitchModel_WrapsAround(t *testing.T) {
	m := newPickerTestModel(testProviders, []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-20250514"}, "")
	m.modelName = "anthropic/claude-sonnet-4-20250514"
	m.favIdx = 1

	m.switchModel(1)
	if m.modelName != "openai/gpt-4o" {
		t.Fatalf("modelName = %q, want openai/gpt-4o (wrap)", m.modelName)
	}
	if m.favIdx != 0 {
		t.Fatalf("favIdx = %d, want 0 (wrap)", m.favIdx)
	}
}

func TestSwitchModel_BackwardWrap(t *testing.T) {
	m := newPickerTestModel(testProviders, []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-20250514"}, "")
	m.modelName = "openai/gpt-4o"
	m.favIdx = 0

	m.switchModel(-1)
	if m.modelName != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("modelName = %q, want anthropic/claude-sonnet-4-20250514 (backward wrap)", m.modelName)
	}
}

func TestSwitchModel_FallsBackToAllModels(t *testing.T) {
	allModels := []string{"openai/gpt-4o", "openai/gpt-4o-mini", "anthropic/claude-sonnet-4-20250514", "anthropic/claude-haiku"}
	m := newModel(nil, "openai/gpt-4o", "", allModels, nil, testProviders, nil, nil, nil, "", nil)
	m.width = 120
	m.height = 40

	m.switchModel(1)
	if m.modelName != "openai/gpt-4o-mini" {
		t.Fatalf("modelName = %q, want openai/gpt-4o-mini", m.modelName)
	}
}

func TestSwitchModel_ChangesChannel(t *testing.T) {
	ch := make(chan string, 8)
	m := newModel(nil, "test/model", "", []string{"test/model"}, ch, testProviders, nil,
		[]string{"openai/gpt-4o", "anthropic/claude-sonnet-4-20250514"}, nil, "", nil)
	m.modelName = "openai/gpt-4o"
	m.favIdx = 0

	m.switchModel(1)

	select {
	case v := <-ch:
		if v != "anthropic/claude-sonnet-4-20250514" {
			t.Fatalf("channel got %q, want anthropic/claude-sonnet-4-20250514", v)
		}
	default:
		t.Fatal("expected model change on channel")
	}
}

// ─── Picker keyboard: 'f' and 'd' keys ─────────────────────────────────

func TestPickerKeyF_TogglesFavorite(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.openModelPicker()

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}
	result, _ := m.updatePicker(msg)
	m2 := result.(TuiModel)

	if !m2.favoriteSet["openai/gpt-4o"] {
		t.Fatal("expected 'f' to toggle favorite on first model")
	}
}

func TestPickerKeyD_SetsDefault(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.openModelPicker()

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}
	result, _ := m.updatePicker(msg)
	m2 := result.(TuiModel)

	if m2.defaultModel != "openai/gpt-4o" {
		t.Fatalf("defaultModel = %q, want openai/gpt-4o after 'd'", m2.defaultModel)
	}
}

func TestPickerKeyEnter_SelectsModel(t *testing.T) {
	ch := make(chan string, 8)
	m := newModel(nil, "other/model", "", []string{"test/model", "other/model"}, ch, testProviders, nil,
		nil, nil, "", nil)
	m.width = 120
	m.height = 40
	m.openModelPicker()
	// cursor at 0 => openai/gpt-4o

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	result, _ := m.updatePicker(msg)
	m2 := result.(TuiModel)

	if m2.modelName != "openai/gpt-4o" {
		t.Fatalf("modelName = %q, want openai/gpt-4o", m2.modelName)
	}
	if m2.pickerActive {
		t.Fatal("picker should be closed after Enter")
	}
}

func TestPickerKeyEscape_ClosesPicker(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.openModelPicker()

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, _ := m.updatePicker(msg)
	m2 := result.(TuiModel)

	if m2.pickerActive {
		t.Fatal("picker should be closed after Escape")
	}
	// model should not change
	if m2.modelName != "test/model" {
		t.Fatalf("modelName = %q, should not change on Escape", m2.modelName)
	}
}

// ─── Picker navigation ─────────────────────────────────────────────────

func TestPickerUpDown_Navigates(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.openModelPicker()

	// Down
	result, _ := m.updatePicker(tea.KeyMsg{Type: tea.KeyDown})
	m2 := result.(TuiModel)
	if m2.pickerCursor != 1 {
		t.Fatalf("cursor = %d after Down, want 1", m2.pickerCursor)
	}

	// Up
	result, _ = m2.updatePicker(tea.KeyMsg{Type: tea.KeyUp})
	m3 := result.(TuiModel)
	if m3.pickerCursor != 0 {
		t.Fatalf("cursor = %d after Up, want 0", m3.pickerCursor)
	}
}

func TestPickerUpDown_ClampBounds(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.openModelPicker()

	// Up at top — should stay at 0
	result, _ := m.updatePicker(tea.KeyMsg{Type: tea.KeyUp})
	m2 := result.(TuiModel)
	if m2.pickerCursor != 0 {
		t.Fatalf("cursor = %d, should clamp at 0", m2.pickerCursor)
	}

	// Down past end
	result, _ = m2.updatePicker(tea.KeyMsg{Type: tea.KeyDown})
	result, _ = result.(TuiModel).updatePicker(tea.KeyMsg{Type: tea.KeyDown})
	result, _ = result.(TuiModel).updatePicker(tea.KeyMsg{Type: tea.KeyDown})
	m5 := result.(TuiModel)
	if m5.pickerCursor != 3 { // 4 models total (0..3)
		t.Fatalf("cursor = %d, should clamp at 3", m5.pickerCursor)
	}
}

// ─── Picker filter ─────────────────────────────────────────────────────

func TestPickerFilter_FiltersModels(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.openModelPicker()

	// Type 'claude'
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c', 'l', 'a', 'u', 'd', 'e'}}
	result, _ := m.updatePicker(msg)
	m2 := result.(TuiModel)

	if m2.pickerFilter != "claude" {
		t.Fatalf("filter = %q, want claude", m2.pickerFilter)
	}
	if m2.pickerModelCount() != 2 { // claude-sonnet + claude-haiku
		t.Fatalf("model count = %d, want 2 (claude models)", m2.pickerModelCount())
	}
}

func TestPickerFilter_BackspaceClearsChar(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.openModelPicker()
	m.pickerFilter = "gp"
	m.rebuildPickerItems()

	result, _ := m.updatePicker(tea.KeyMsg{Type: tea.KeyBackspace})
	m2 := result.(TuiModel)

	if m2.pickerFilter != "g" {
		t.Fatalf("filter = %q, want g", m2.pickerFilter)
	}
}

// ─── Picker view rendering ─────────────────────────────────────────────

func TestPickerView_ShowsFavoriteMarker(t *testing.T) {
	m := newPickerTestModel(testProviders, []string{"openai/gpt-4o"}, "")
	m.openModelPicker()

	view := m.renderModelPickerContent()
	if !containsANSI(view, "★") {
		t.Fatal("picker view should contain ★ for favorite model")
	}
}

func TestPickerView_ShowsDefaultMarker(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "openai/gpt-4o")
	m.openModelPicker()

	view := m.renderModelPickerContent()
	if !containsANSI(view, "◆") {
		t.Fatal("picker view should contain ◆ for default model")
	}
}

func TestPickerView_NoMarkersWhenEmpty(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.openModelPicker()

	view := m.renderModelPickerContent()
	if containsANSI(view, "★") {
		t.Fatal("picker view should not contain ★ when no favorites")
	}
	if containsANSI(view, "◆") {
		t.Fatal("picker view should not contain ◆ when no default")
	}
}

func TestPickerView_ShowsHint(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.openModelPicker()

	view := m.renderModelPickerContent()
	if !containsANSI(view, "f fav") {
		t.Fatal("picker hint should mention 'f fav'")
	}
	if !containsANSI(view, "d default") {
		t.Fatal("picker hint should mention 'd default'")
	}
}

func TestPickerView_RightBorderVisible(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.openModelPicker()

	lines := strings.Split(stripANSI(m.renderModelPickerContent()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected picker content to have multiple lines, got %d", len(lines))
	}
	for i, line := range lines {
		if line == "" {
			continue
		}
		if lipgloss.Width(line) > 80 {
			t.Fatalf("line %d width = %d, want <= 80: %q", i, lipgloss.Width(line), line)
		}
		if i != 0 && i != len(lines)-1 && !strings.HasSuffix(line, "│") {
			t.Fatalf("line %d should end with right border: %q", i, line)
		}
	}
}

// containsANSI checks if s contains substr, ignoring ANSI escape sequences.
func containsANSI(s, substr string) bool {
	// Simple approach: just check raw string since ★ and ◆ are plain UTF-8
	return len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ─── Picker model selection updates favIdx ─────────────────────────────

func TestSelectModel_UpdatesFavIdx(t *testing.T) {
	m := newPickerTestModel(testProviders, []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-20250514"}, "")
	m.openModelPicker()
	m.pickerCursor = 2 // anthropic/claude-sonnet-4-20250514

	m.selectModel("anthropic/claude-sonnet-4-20250514")

	if m.favIdx != 1 {
		t.Fatalf("favIdx = %d, want 1", m.favIdx)
	}
	if m.modelName != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("modelName = %q, want anthropic/claude-sonnet-4-20250514", m.modelName)
	}
}

func TestSelectModel_UpdatesModelIdx(t *testing.T) {
	allModels := []string{"openai/gpt-4o", "openai/gpt-4o-mini", "anthropic/claude-sonnet-4-20250514", "anthropic/claude-haiku"}
	m := newModel(nil, "openai/gpt-4o", "", allModels, nil, testProviders, nil, nil, nil, "", nil)
	m.width = 120
	m.height = 40
	m.openModelPicker()
	m.pickerCursor = 1 // openai/gpt-4o-mini

	m.selectModel("openai/gpt-4o-mini")

	if m.modelIdx != 1 {
		t.Fatalf("modelIdx = %d, want 1", m.modelIdx)
	}
}

// ─── newModel initializes favorites and default ────────────────────────

func TestNewModel_SetsFavoriteFields(t *testing.T) {
	favs := []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-20250514"}
	m := newModel(nil, "openai/gpt-4o", "", favs, nil, testProviders, nil,
		favs, nil, "", nil)

	if len(m.favoriteModels) != 2 {
		t.Fatalf("favoriteModels len = %d, want 2", len(m.favoriteModels))
	}
	if !m.favoriteSet["openai/gpt-4o"] {
		t.Fatal("expected openai/gpt-4o in favoriteSet")
	}
	if m.favIdx != 0 {
		t.Fatalf("favIdx = %d, want 0", m.favIdx)
	}
}

func TestNewModel_SetsDefaultModel(t *testing.T) {
	m := newModel(nil, "test/model", "", nil, nil, testProviders, nil,
		nil, nil, "anthropic/claude-sonnet-4-20250514", nil)

	if m.defaultModel != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("defaultModel = %q, want anthropic/claude-sonnet-4-20250514", m.defaultModel)
	}
}

func TestNewModel_FavIdxMatchesModelName(t *testing.T) {
	favs := []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-20250514"}
	m := newModel(nil, "anthropic/claude-sonnet-4-20250514", "", favs, nil, testProviders, nil,
		favs, nil, "", nil)

	if m.favIdx != 1 {
		t.Fatalf("favIdx = %d, want 1 (matching modelName)", m.favIdx)
	}
}
