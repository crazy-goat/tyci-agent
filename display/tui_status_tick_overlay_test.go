package display

import "testing"

// Issue #319: a statusTickMsg that arrives while an overlay is open must keep
// the chain alive instead of being swallowed by the overlay's handler.
func TestStatusTick_SurvivesOpenOverlay(t *testing.T) {
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.width = 100
	m.height = 40
	m.ready = true
	m.reading = false
	m.statusTickArmed = true
	m.historySearchActive = true

	_, cmd := m.Update(statusTickMsg{})
	if cmd == nil {
		t.Fatal("tick with an overlay open must return the next tick cmd")
	}
}
