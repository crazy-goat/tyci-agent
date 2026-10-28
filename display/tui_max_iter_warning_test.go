package display

import (
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The agent sends the max-iterations warning through TUI.ToolBlock. This test
// runs that call through a real bubbletea program, so the "⏳" filter in
// ToolBlock and the block rendering both run. The warning must reach the
// model's blocks and its rendered view.
func TestTUI_ToolBlockShowsMaxIterationsWarning(t *testing.T) {
	const warning = "⚠️ Agent executed 3 tool-call iterations – possible infinite loop. Stopping."

	sized, _ := newModel(nil, "test/model", "", nil, 0, 0, 0).Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model := sized.(TuiModel)
	p := tea.NewProgram(model, tea.WithInput(nil), tea.WithoutRenderer(), tea.WithOutput(io.Discard))
	done := make(chan tea.Model, 1)
	go func() {
		final, _ := p.Run()
		done <- final
	}()

	tui := &TUI{
		prog:      p,
		done:      make(chan struct{}),
		flushWake: make(chan struct{}, 1),
		flushDone: make(chan struct{}),
	}
	tui.ToolBlock(warning)
	// Quit is queued after the block message, so the final model has seen it.
	p.Quit()
	final := (<-done).(TuiModel)

	found := false
	for _, b := range final.blocks {
		if b.kind == "block" && strings.Contains(b.content, "tool-call iterations") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warning not in TUI blocks: %+v", final.blocks)
	}
	if view := final.View(); !strings.Contains(view, "tool-call iterations") {
		t.Errorf("warning not in rendered view: %q", view)
	}
}
