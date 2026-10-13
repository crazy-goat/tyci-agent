package display

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// focusedSidebarModel returns a sidebar test model at 120 columns with the
// sidebar open and focused, so that every message goes through
// routeSidebarMsg and updateSidebar.
func focusedSidebarModel() TuiModel {
	m := sidebarWidthModel(120)
	m.sidebarFocused = true
	return m
}

// TestSidebarFocused_ResizeFlushIsHandled checks that the debounced resize
// flush is not dropped while the sidebar has focus. Otherwise resizePending
// stays true and later resizes never schedule a flush.
func TestSidebarFocused_ResizeFlushIsHandled(t *testing.T) {
	useTempHome(t)
	m := focusedSidebarModel()

	m = pressKey(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	if !m.resizePending {
		t.Fatalf("test premise broken: a resize did not set resizePending")
	}

	m = pressKey(t, m, resizeFlushMsg{})
	if m.resizePending {
		t.Fatalf("resizeFlushMsg was dropped while the sidebar is focused: resizePending is still true")
	}
}

// TestSidebarFocused_StatusTickIsHandled checks that the status tick is
// handled while the sidebar has focus, so the armed flag is cleared when no
// turn or live job needs a repaint.
func TestSidebarFocused_StatusTickIsHandled(t *testing.T) {
	useTempHome(t)
	m := focusedSidebarModel()
	m.statusTickArmed = true

	m = pressKey(t, m, statusTickMsg{})
	if m.statusTickArmed {
		t.Fatalf("statusTickMsg was dropped while the sidebar is focused: statusTickArmed is still true")
	}
}

// TestSidebarFocused_WindowSizeCallsHandleResize checks that a terminal size
// change while the sidebar has focus goes through handleResize, which sets
// the resize state and schedules the flush.
func TestSidebarFocused_WindowSizeCallsHandleResize(t *testing.T) {
	useTempHome(t)
	m := focusedSidebarModel()

	m = pressKey(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	if m.resizeWidth != 140 || m.resizeHeight != 30 {
		t.Fatalf("resize state not updated: got %dx%d, want 140x30", m.resizeWidth, m.resizeHeight)
	}
	if !m.resizePending {
		t.Fatalf("handleResize did not schedule the resize flush")
	}
}

// TestQuit_SavesPendingSidebarWidth checks that a width change made less than
// sidebarWidthSaveDelay before quit is written by saveSidebarWidthOnExit,
// because its debounce timer never fires.
func TestQuit_SavesPendingSidebarWidth(t *testing.T) {
	useTempHome(t)
	m := focusedSidebarModel()

	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	if !m.pendingSidebarWidthSave {
		t.Fatalf("a width key press did not set pendingSidebarWidthSave")
	}

	m.saveSidebarWidthOnExit()
	if got := loadSidebarWidthPercent(); got != m.sidebarWidthPercent {
		t.Fatalf("saved width %v on quit, want %v", got, m.sidebarWidthPercent)
	}
}

// TestQuit_NoPendingSidebarWidthWritesNothing checks that quitting without a
// pending width change does not create the state file.
func TestQuit_NoPendingSidebarWidthWritesNothing(t *testing.T) {
	home := useTempHome(t)
	m := focusedSidebarModel()

	m.saveSidebarWidthOnExit()
	path := filepath.Join(home, ".tyci", tuiStateFile)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state file written on quit without a pending width change")
	}
}
