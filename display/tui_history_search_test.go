package display

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// helper: build a TuiModel with history for history search tests.
func newHistorySearchTestModel(history []string) TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 120
	m.height = 40
	m.ready = true
	m.inputHistory = history
	return m
}

var testHistory = []string{
	"hello world",
	"how are you",
	"search this",
	"another search term",
	"hello again",
	"find me",
	"SEARCH CAPS",
}

// ─── Open/Close state transitions ──────────────────────────────────────

func TestHistorySearch_Open(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.input.SetValue("current input")
	m.openHistorySearch()

	if !m.historySearchActive {
		t.Fatal("expected historySearchActive to be true")
	}
	if m.stashedSearchInput != "current input" {
		t.Fatalf("stashedSearchInput = %q, want 'current input'", m.stashedSearchInput)
	}
	if m.historySearchFilter != "" {
		t.Fatalf("filter = %q, want empty", m.historySearchFilter)
	}
	if m.historySearchCursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.historySearchCursor)
	}
}

func TestHistorySearch_CloseRestoresInput(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.input.SetValue("my input")
	m.openHistorySearch()

	// Simulate typing a filter
	m.historySearchFilter = "hello"
	m.rebuildHistorySearchResults()

	m.closeHistorySearch()

	if m.historySearchActive {
		t.Fatal("expected historySearchActive to be false after close")
	}
	if m.input.Value() != "my input" {
		t.Fatalf("input = %q, want 'my input' (restored)", m.input.Value())
	}
	if m.historySearchResults != nil {
		t.Fatal("expected historySearchResults to be nil after close")
	}
	if m.stashedSearchInput != "" {
		t.Fatal("expected stashedSearchInput to be cleared after close")
	}
}

func TestHistorySearch_SelectReplacesInput(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.input.SetValue("my input")
	m.openHistorySearch()

	m.selectHistorySearchEntry("selected entry")

	if m.historySearchActive {
		t.Fatal("expected historySearchActive to be false after select")
	}
	if m.input.Value() != "selected entry" {
		t.Fatalf("input = %q, want 'selected entry'", m.input.Value())
	}
	if m.stashedSearchInput != "" {
		t.Fatal("expected stashedSearchInput to be cleared after select")
	}
}

// ─── Filter narrowing ──────────────────────────────────────────────────

func TestHistorySearch_EmptyFilterShowsAll(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()

	// All history entries, newest first
	if len(m.historySearchResults) != len(testHistory) {
		t.Fatalf("results = %d, want %d", len(m.historySearchResults), len(testHistory))
	}
	// Newest first: last entry in inputHistory should be first in results
	if m.historySearchResults[0] != testHistory[len(testHistory)-1] {
		t.Fatalf("results[0] = %q, want %q (newest first)", m.historySearchResults[0], testHistory[len(testHistory)-1])
	}
}

func TestHistorySearch_FilterNarrows(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()

	// Type "hello"
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h', 'e', 'l', 'l', 'o'}}
	result, _ := m.updateHistorySearch(msg)
	m2 := result.(TuiModel)

	if m2.historySearchFilter != "hello" {
		t.Fatalf("filter = %q, want 'hello'", m2.historySearchFilter)
	}
	// "hello world" and "hello again" match, newest first
	if len(m2.historySearchResults) != 2 {
		t.Fatalf("results = %d, want 2 (hello matches)", len(m2.historySearchResults))
	}
}

func TestHistorySearch_FilterCaseInsensitive(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s', 'e', 'a', 'r', 'c', 'h'}}
	result, _ := m.updateHistorySearch(msg)
	m2 := result.(TuiModel)

	// "search this", "another search term", "SEARCH CAPS" = 3 matches
	if len(m2.historySearchResults) != 3 {
		t.Fatalf("results = %d, want 3 (case-insensitive 'search')", len(m2.historySearchResults))
	}
}

func TestHistorySearch_FilterNoMatch(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z', 'z', 'z'}}
	result, _ := m.updateHistorySearch(msg)
	m2 := result.(TuiModel)

	if len(m2.historySearchResults) != 0 {
		t.Fatalf("results = %d, want 0", len(m2.historySearchResults))
	}
}

func TestHistorySearch_BackspaceClearsChar(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()
	m.historySearchFilter = "he"
	m.rebuildHistorySearchResults()

	result, _ := m.updateHistorySearch(tea.KeyMsg{Type: tea.KeyBackspace})
	m2 := result.(TuiModel)

	if m2.historySearchFilter != "h" {
		t.Fatalf("filter = %q, want 'h'", m2.historySearchFilter)
	}
}

func TestHistorySearch_BackspaceOnEmptyFilter(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()

	result, _ := m.updateHistorySearch(tea.KeyMsg{Type: tea.KeyBackspace})
	m2 := result.(TuiModel)

	if m2.historySearchFilter != "" {
		t.Fatalf("filter = %q, want '' (backspace on empty is noop)", m2.historySearchFilter)
	}
}

// ─── Cursor bounds ─────────────────────────────────────────────────────

func TestHistorySearch_CursorClampedOnFilterChange(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()
	// Move cursor far down
	m.historySearchCursor = 5

	// Type "hello" - narrows to 2 results
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h', 'e', 'l', 'l', 'o'}}
	result, _ := m.updateHistorySearch(msg)
	m2 := result.(TuiModel)

	if m2.historySearchCursor >= len(m2.historySearchResults) {
		t.Fatalf("cursor = %d, should be clamped to < %d", m2.historySearchCursor, len(m2.historySearchResults))
	}
}

func TestHistorySearch_UpDownNavigation(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()

	// Down
	result, _ := m.updateHistorySearch(tea.KeyMsg{Type: tea.KeyDown})
	m2 := result.(TuiModel)
	if m2.historySearchCursor != 1 {
		t.Fatalf("cursor = %d after Down, want 1", m2.historySearchCursor)
	}

	// Up
	result, _ = m2.updateHistorySearch(tea.KeyMsg{Type: tea.KeyUp})
	m3 := result.(TuiModel)
	if m3.historySearchCursor != 0 {
		t.Fatalf("cursor = %d after Up, want 0", m3.historySearchCursor)
	}
}

func TestHistorySearch_UpDownClampBounds(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()

	// Up at top — should stay at 0
	result, _ := m.updateHistorySearch(tea.KeyMsg{Type: tea.KeyUp})
	m2 := result.(TuiModel)
	if m2.historySearchCursor != 0 {
		t.Fatalf("cursor = %d, should clamp at 0", m2.historySearchCursor)
	}

	// Down past end
	for i := 0; i < len(testHistory)+5; i++ {
		result, _ = result.(TuiModel).updateHistorySearch(tea.KeyMsg{Type: tea.KeyDown})
	}
	mEnd := result.(TuiModel)
	if mEnd.historySearchCursor != len(testHistory)-1 {
		t.Fatalf("cursor = %d, should clamp at %d", mEnd.historySearchCursor, len(testHistory)-1)
	}
}

func TestHistorySearch_HomeEnd(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()
	m.historySearchCursor = 3

	result, _ := m.updateHistorySearch(tea.KeyMsg{Type: tea.KeyHome})
	m2 := result.(TuiModel)
	if m2.historySearchCursor != 0 {
		t.Fatalf("cursor = %d after Home, want 0", m2.historySearchCursor)
	}

	result, _ = m2.updateHistorySearch(tea.KeyMsg{Type: tea.KeyEnd})
	m3 := result.(TuiModel)
	if m3.historySearchCursor != len(testHistory)-1 {
		t.Fatalf("cursor = %d after End, want %d", m3.historySearchCursor, len(testHistory)-1)
	}
}

func TestHistorySearch_PgUpDown(t *testing.T) {
	// Create enough history entries
	longHistory := make([]string, 30)
	for i := range longHistory {
		longHistory[i] = "entry " + string(rune('A'+i%26)) + string(rune('0'+i/26))
	}
	m := newHistorySearchTestModel(longHistory)
	m.openHistorySearch()
	m.historySearchCursor = 15

	result, _ := m.updateHistorySearch(tea.KeyMsg{Type: tea.KeyPgUp})
	m2 := result.(TuiModel)
	if m2.historySearchCursor != 5 {
		t.Fatalf("cursor = %d after PgUp from 15, want 5", m2.historySearchCursor)
	}

	result, _ = m2.updateHistorySearch(tea.KeyMsg{Type: tea.KeyPgDown})
	m3 := result.(TuiModel)
	if m3.historySearchCursor != 15 {
		t.Fatalf("cursor = %d after PgDown from 5, want 15", m3.historySearchCursor)
	}
}

// ─── Enter vs Esc restoration ──────────────────────────────────────────

func TestHistorySearch_EscapeRestoresInput(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.input.SetValue("original text")
	m.openHistorySearch()

	// Type something to change filter
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h', 'e', 'l', 'l', 'o'}}
	result, _ := m.updateHistorySearch(msg)
	m2 := result.(TuiModel)

	// Press Escape
	result, _ = m2.updateHistorySearch(tea.KeyMsg{Type: tea.KeyEscape})
	m3 := result.(TuiModel)

	if m3.historySearchActive {
		t.Fatal("expected modal closed on Esc")
	}
	if m3.input.Value() != "original text" {
		t.Fatalf("input = %q, want 'original text' (restored on Esc)", m3.input.Value())
	}
}

func TestHistorySearch_EnterSelectsEntry(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.input.SetValue("original")
	m.openHistorySearch()

	// Select first result (newest: "SEARCH CAPS")
	result, _ := m.updateHistorySearch(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(TuiModel)

	if m2.historySearchActive {
		t.Fatal("expected modal closed on Enter")
	}
	if m2.input.Value() != testHistory[len(testHistory)-1] {
		t.Fatalf("input = %q, want %q (selected)", m2.input.Value(), testHistory[len(testHistory)-1])
	}
}

func TestHistorySearch_EnterWithFilteredSelectsCorrectEntry(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.input.SetValue("original")
	m.openHistorySearch()

	// Filter to "hello" → results: ["hello again", "hello world"] (newest first)
	typeRunes := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h', 'e', 'l', 'l', 'o'}}
	result, _ := m.updateHistorySearch(typeRunes)
	m2 := result.(TuiModel)

	// Move cursor down to 1 (second result: "hello world")
	result, _ = m2.updateHistorySearch(tea.KeyMsg{Type: tea.KeyDown})
	m3 := result.(TuiModel)

	// Press Enter
	result, _ = m3.updateHistorySearch(tea.KeyMsg{Type: tea.KeyEnter})
	m4 := result.(TuiModel)

	if m4.input.Value() != "hello world" {
		t.Fatalf("input = %q, want 'hello world'", m4.input.Value())
	}
}

// ─── Empty history ─────────────────────────────────────────────────────

func TestHistorySearch_EmptyHistory(t *testing.T) {
	m := newHistorySearchTestModel(nil)
	m.openHistorySearch()

	if len(m.historySearchResults) != 0 {
		t.Fatalf("results = %d, want 0 with empty history", len(m.historySearchResults))
	}

	// Enter on empty results should be noop
	result, _ := m.updateHistorySearch(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(TuiModel)
	if !m2.historySearchActive {
		t.Fatal("modal should stay open when Enter on empty results")
	}

	// Down/Up should not crash
	result, _ = m2.updateHistorySearch(tea.KeyMsg{Type: tea.KeyDown})
	m3 := result.(TuiModel)
	if m3.historySearchCursor != 0 {
		t.Fatalf("cursor = %d, want 0 on empty results", m3.historySearchCursor)
	}
}

// ─── Single result ─────────────────────────────────────────────────────

func TestHistorySearch_SingleResult(t *testing.T) {
	m := newHistorySearchTestModel([]string{"unique entry"})
	m.openHistorySearch()

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u', 'n', 'i', 'q'}}
	result, _ := m.updateHistorySearch(msg)
	m2 := result.(TuiModel)

	if len(m2.historySearchResults) != 1 {
		t.Fatalf("results = %d, want 1", len(m2.historySearchResults))
	}
	if m2.historySearchCursor != 0 {
		t.Fatalf("cursor = %d, want 0 for single result", m2.historySearchCursor)
	}

	// Enter should select it
	result, _ = m2.updateHistorySearch(tea.KeyMsg{Type: tea.KeyEnter})
	m3 := result.(TuiModel)
	if m3.input.Value() != "unique entry" {
		t.Fatalf("input = %q, want 'unique entry'", m3.input.Value())
	}
}

// ─── Ctrl+R cycles through results ─────────────────────────────────────

func TestHistorySearch_CtrlRCycles(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()
	// Filter to "hello" → ["hello again", "hello world"]
	m.historySearchFilter = "hello"
	m.rebuildHistorySearchResults()

	if m.historySearchCursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.historySearchCursor)
	}

	// First Ctrl+R → cursor 1
	result, _ := m.updateHistorySearch(tea.KeyMsg{Type: tea.KeyCtrlR})
	m2 := result.(TuiModel)
	if m2.historySearchCursor != 1 {
		t.Fatalf("cursor = %d after 1st Ctrl+R, want 1", m2.historySearchCursor)
	}

	// Second Ctrl+R → wraps to 0
	result, _ = m2.updateHistorySearch(tea.KeyMsg{Type: tea.KeyCtrlR})
	m3 := result.(TuiModel)
	if m3.historySearchCursor != 0 {
		t.Fatalf("cursor = %d after 2nd Ctrl+R, want 0 (wrap)", m3.historySearchCursor)
	}
}

func TestHistorySearch_CtrlRWithEmptyResults(t *testing.T) {
	m := newHistorySearchTestModel(nil)
	m.openHistorySearch()

	// Should not crash
	result, _ := m.updateHistorySearch(tea.KeyMsg{Type: tea.KeyCtrlR})
	m2 := result.(TuiModel)
	if m2.historySearchCursor != 0 {
		t.Fatalf("cursor = %d, want 0 on empty", m2.historySearchCursor)
	}
}

// ─── Space in filter ───────────────────────────────────────────────────

func TestHistorySearch_SpaceInFilter(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()

	result, _ := m.updateHistorySearch(tea.KeyMsg{Type: tea.KeySpace})
	m2 := result.(TuiModel)

	if m2.historySearchFilter != " " {
		t.Fatalf("filter = %q, want ' '", m2.historySearchFilter)
	}
}

// ─── Mouse events swallowed ────────────────────────────────────────────

func TestHistorySearch_MouseSwallowed(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()

	// Click should be swallowed
	result, _ := m.updateHistorySearch(tea.MouseMsg{Button: tea.MouseButtonLeft})
	m2 := result.(TuiModel)
	if !m2.historySearchActive {
		t.Fatal("modal should remain active after mouse click")
	}

	// Scroll wheel up
	result, _ = m2.updateHistorySearch(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m3 := result.(TuiModel)
	if m3.historySearchCursor != 0 {
		t.Fatalf("cursor = %d after scroll up at top, want 0", m3.historySearchCursor)
	}

	// Scroll wheel down
	result, _ = m3.updateHistorySearch(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m4 := result.(TuiModel)
	if m4.historySearchCursor != 1 {
		t.Fatalf("cursor = %d after scroll down, want 1", m4.historySearchCursor)
	}
}

// ─── Tab/ShiftTab swallowed ────────────────────────────────────────────

func TestHistorySearch_TabSwallowed(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()

	result, _ := m.updateHistorySearch(tea.KeyMsg{Type: tea.KeyTab})
	m2 := result.(TuiModel)
	if !m2.historySearchActive {
		t.Fatal("modal should remain active after Tab")
	}
	if m2.historySearchCursor != 0 {
		t.Fatalf("cursor changed after Tab: %d", m2.historySearchCursor)
	}
}

// ─── Window resize ─────────────────────────────────────────────────────

func TestHistorySearch_WindowResize(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()

	result, _ := m.updateHistorySearch(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 := result.(TuiModel)
	if m2.width != 80 || m2.height != 24 {
		t.Fatalf("resize not applied: %dx%d", m2.width, m2.height)
	}
	if !m2.historySearchActive {
		t.Fatal("modal should remain active after resize")
	}
}

// ─── Integration: Ctrl+R via Update routing ────────────────────────────

func TestHistorySearch_CtrlROpensViaUpdate(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.reading = true
	m.input.SetValue("test input")

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m2 := result.(TuiModel)

	if !m2.historySearchActive {
		t.Fatal("Ctrl+R should open history search via Update")
	}
	if m2.stashedSearchInput != "test input" {
		t.Fatalf("stashedSearchInput = %q, want 'test input'", m2.stashedSearchInput)
	}
}

func TestHistorySearch_EscapeClosesViaUpdate(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.reading = true
	m.input.SetValue("preserved")
	m.openHistorySearch()

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := result.(TuiModel)

	if m2.historySearchActive {
		t.Fatal("Esc should close history search via Update")
	}
	if m2.input.Value() != "preserved" {
		t.Fatalf("input = %q, want 'preserved'", m2.input.Value())
	}
}

func TestHistorySearch_EnterSelectsViaUpdate(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.reading = true
	m.input.SetValue("")
	m.openHistorySearch()

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(TuiModel)

	if m2.historySearchActive {
		t.Fatal("Enter should close history search via Update")
	}
	// Should have selected the newest entry
	if m2.input.Value() != testHistory[len(testHistory)-1] {
		t.Fatalf("input = %q, want %q", m2.input.Value(), testHistory[len(testHistory)-1])
	}
}

// ─── View rendering ────────────────────────────────────────────────────

func TestHistorySearch_ViewRendered(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()

	view := m.View()
	if view == "" {
		t.Fatal("expected non-empty view when search is active")
	}
	// Should contain the title
	if !strings.Contains(view, "History Search") {
		t.Fatal("view should contain 'History Search' title")
	}
}

func TestHistorySearch_ViewShowsFilteredResults(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()
	m.historySearchFilter = "hello"
	m.rebuildHistorySearchResults()

	view := m.renderHistorySearchContent()
	if !strings.Contains(view, "hello") {
		t.Fatal("view should contain filtered results")
	}
}

func TestHistorySearch_ViewShowsNoResults(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.openHistorySearch()
	m.historySearchFilter = "zzzzz"
	m.rebuildHistorySearchResults()

	view := m.renderHistorySearchContent()
	if !strings.Contains(view, "No matching") {
		t.Fatal("view should show 'No matching' message")
	}
}

func TestHistorySearch_ViewNarrowTerminal(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.width = 40
	m.height = 20
	m.openHistorySearch()

	view := m.renderHistorySearchContent()
	// Should not crash and should produce output
	if view == "" {
		t.Fatal("expected non-empty view on narrow terminal")
	}
}

// ─── Keyboard integration: Ctrl+R not leaked to textarea ───────────────

func TestHistorySearch_CtrlRNotPassedToTextarea(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.reading = true
	m.input.SetValue("should not change")

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m2 := result.(TuiModel)

	// The textarea value should not change (Ctrl+R consumed before textarea)
	if m2.input.Value() != "should not change" {
		t.Fatalf("input value changed: %q", m2.input.Value())
	}
}

// ─── Update routing priority: history search above picker ──────────────

func TestHistorySearch_RoutedBeforePicker(t *testing.T) {
	m := newHistorySearchTestModel(testHistory)
	m.pickerActive = true // both active (shouldn't happen, but test priority)
	m.openHistorySearch()

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := result.(TuiModel)

	// History search should be handled (closed), not the picker
	if m2.historySearchActive {
		t.Fatal("history search should have been closed by Escape")
	}
}
