package display

// sidebarSaveVisible is the callback production main() wires in via
// TUI.SetSidebarPersister; nil (tests, unwired runs) means "don't persist" —
// a unit test constructing TuiModel directly must never write the developer's
// real ~/.tyci/config.json (same wiring posture as sessionLister).
//
// Read and written only from the bubbletea Update goroutine: set via
// tuiSetSidebarPersisterMsg, read on sidebar open/close — no lock needed.
var sidebarSaveVisible func(bool)

// tuiSetSidebarPersisterMsg carries the persistence callback from
// TUI.SetSidebarPersister into the bubbletea goroutine — same cross-goroutine
// message-send pattern as every other model mutation from main()
// (see tuiSetSessionListerMsg).
type tuiSetSidebarPersisterMsg struct {
	fn func(bool)
}

// applySidebarVisibilityChange is the one place every open/close of the
// sidebar funnels through: it persists the new visibility when a persister
// has been wired. Write errors are deliberately swallowed (the
// favorites/default-model setters in commands.go do the same): a failed
// config write must not break the key press that toggled the sidebar. The
// write itself runs off the event loop so a slow disk never costs a keypress.
func (m *TuiModel) persistSidebarVisible(visible bool) {
	if sidebarSaveVisible == nil {
		return
	}
	go sidebarSaveVisible(visible)
}

// SetSidebarPersister wires production main()'s persistence callback
// (agent.SetSidebarVisible). Called once from commands.go before Run(),
// outside the bubbletea event-loop goroutine, so it goes through the same
// message-send pattern as every other cross-goroutine model mutation here.
func (t *TUI) SetSidebarPersister(fn func(bool)) {
	t.prog.Send(tuiSetSidebarPersisterMsg{fn: fn})
}
