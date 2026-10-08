package display

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// sessionsTestModel returns a model with the sidebar open on the Sessions tab.
// The lister counts its calls through calls.
func sessionsTestModel(calls *int, entries []TuiResumeEntry) TuiModel {
	m := newTestModelForSidebar()
	m.sidebarActive = true
	m.sidebarTab = sidebarTabSessions
	m.sessionLister = func() []TuiResumeEntry {
		*calls++
		return entries
	}
	return m
}

// Rendering the Sessions tab must not read the session files. Before, every
// View read them on the Bubble Tea goroutine, which froze the TUI.
func TestSidebarSessions_RenderReadsCacheOnly(t *testing.T) {
	calls := 0
	m := sessionsTestModel(&calls, mkEntries(2))
	for i := 0; i < 5; i++ {
		_ = m.sidebarTabLines(60)
		_ = m.sidebarRowCount()
	}
	if calls != 0 {
		t.Fatalf("rendering read the session list %d times, want 0", calls)
	}
	if got := strings.Join(m.sidebarTabLines(60), "\n"); !strings.Contains(got, "Loading sessions") {
		t.Fatalf("before the first load: %q", got)
	}
}

// The load runs in the returned command, sorts newest-first, and is cached
// for sidebarSessionsTTL.
func TestSidebarSessions_LoadIsCachedAndNewestFirst(t *testing.T) {
	calls := 0
	entries := mkEntries(3) // ModTime grows with the index: the last one is the newest
	m := sessionsTestModel(&calls, entries)

	cmd := m.sidebarSessionsCmd()
	if cmd == nil {
		t.Fatal("expected a load when the Sessions tab is open")
	}
	if !m.sessionsLoading {
		t.Fatal("load started but not marked as loading")
	}
	if m.sidebarSessionsCmd() != nil {
		t.Fatal("a second load started while one is in flight")
	}
	if calls != 0 {
		t.Fatalf("the lister ran before the command did (%d calls)", calls)
	}

	msg, ok := cmd().(sidebarSessionsMsg)
	if !ok {
		t.Fatalf("command returned %T, want sidebarSessionsMsg", msg)
	}
	model, _ := m.Update(msg)
	m = model.(TuiModel)
	if calls != 1 {
		t.Fatalf("lister calls = %d, want 1", calls)
	}
	if m.sessionsLoading {
		t.Fatal("still marked as loading after the result arrived")
	}
	got := m.sidebarSessionEntries()
	if len(got) != 3 || got[0].Path != entries[2].Path {
		t.Fatalf("entries = %+v, want 3 newest-first", got)
	}
	if strings.Contains(strings.Join(m.sidebarTabLines(60), "\n"), "Loading sessions") {
		t.Fatal("the loaded list still shows the loading hint")
	}

	if m.sidebarSessionsCmd() != nil {
		t.Fatal("reloaded within sidebarSessionsTTL")
	}
	m.sessionsLoadedAt = time.Now().Add(-sidebarSessionsTTL - time.Second)
	if m.sidebarSessionsCmd() == nil {
		t.Fatal("no reload after sidebarSessionsTTL")
	}
}

// A load starts only when the Sessions tab is on screen.
func TestSidebarSessions_LoadOnlyWhenTabVisible(t *testing.T) {
	calls := 0
	m := sessionsTestModel(&calls, mkEntries(1))

	m.sidebarActive = false
	if m.sidebarSessionsCmd() != nil {
		t.Fatal("load started with the sidebar closed")
	}
	m.sidebarActive = true
	m.sidebarTab = sidebarTabTokens
	if m.sidebarSessionsCmd() != nil {
		t.Fatal("load started on another tab")
	}
}

// Update starts the load after a message that opens the Sessions tab, so the
// list is ready when the user looks at it.
func TestSidebarSessions_UpdateStartsLoadOnTabSwitch(t *testing.T) {
	calls := 0
	m := sessionsTestModel(&calls, mkEntries(1))
	m.sidebarTab = sidebarTabTokens
	m.sidebarFocused = true

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	next := model.(TuiModel)
	if next.sidebarTab != sidebarTabSessions {
		t.Fatalf("tab = %d, want the Sessions tab", next.sidebarTab)
	}
	if !next.sessionsLoading {
		t.Fatal("switching to the Sessions tab did not start a load")
	}
	if calls != 0 {
		t.Fatalf("Update ran the lister on the Bubble Tea goroutine (%d calls)", calls)
	}
}
