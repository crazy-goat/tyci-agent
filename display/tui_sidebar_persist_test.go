package display

import (
	"testing"
	"time"
)

// TestToggleSidebar_PersistsVisibility covers the whole point of
// tui_sidebar_persist.go: Ctrl+T open saves visible=true, Ctrl+T close saves
// visible=false, so the next tyci start reopens (or keeps closed) the
// sidebar. The persister is invoked off the event loop (go ...), so the test
// reads from a channel rather than a plain variable.
func TestToggleSidebar_PersistsVisibility(t *testing.T) {
	m := newTestModelForSidebar()
	saved := make(chan bool, 8)
	prev := sidebarSaveVisible
	sidebarSaveVisible = func(v bool) { saved <- v }
	defer func() { sidebarSaveVisible = prev }()

	m.toggleSidebar()
	if !m.sidebarActive {
		t.Fatal("sidebar should be open after first toggle")
	}
	select {
	case v := <-saved:
		if v != true {
			t.Fatalf("first toggle should persist true, got %v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("first toggle did not persist visibility")
	}

	m.toggleSidebar()
	if m.sidebarActive {
		t.Fatal("sidebar should be closed after second toggle")
	}
	select {
	case v := <-saved:
		if v != false {
			t.Fatalf("second toggle should persist false, got %v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("second toggle did not persist visibility")
	}
}

// TestCloseSidebarPersisted_NoDoubleSave guards against closeSidebarPersisted
// saving on an already-closed sidebar (e.g. Esc racing Ctrl+T).
func TestCloseSidebarPersisted_NoDoubleSave(t *testing.T) {
	m := newTestModelForSidebar()
	saved := make(chan bool, 8)
	prev := sidebarSaveVisible
	sidebarSaveVisible = func(v bool) { saved <- v }
	defer func() { sidebarSaveVisible = prev }()

	m.closeSidebarPersisted() // already closed: must not persist anything
	select {
	case v := <-saved:
		t.Fatalf("close on already-closed sidebar persisted %v", v)
	default:
	}
}

// TestSetSidebarPersisterMsg_WiresCallback checks the Update handler accepts
// the cross-goroutine wiring message the same way tuiSetSessionListerMsg is
// handled.
func TestSetSidebarPersisterMsg_WiresCallback(t *testing.T) {
	m := newTestModelForSidebar()
	fn := func(bool) {}
	if _, cmd := m.Update(tuiSetSidebarPersisterMsg{fn: fn}); cmd != nil {
		t.Fatal("Update should consume tuiSetSidebarPersisterMsg with no cmd")
	}
	if sidebarSaveVisible == nil {
		t.Fatal("persister callback not wired")
	}
	sidebarSaveVisible = nil // don't leak into other tests
}

// TestNewModel_StartsClosedByDefault pins the zero-value startup state: an
// unwired run (tests, missing config) starts with the sidebar closed.
func TestNewModel_StartsClosedByDefault(t *testing.T) {
	m := newTestModelForSidebar()
	if m.sidebarActive {
		t.Fatal("fresh model must start with sidebar closed")
	}
}
