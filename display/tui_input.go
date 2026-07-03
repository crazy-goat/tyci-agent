package display

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m TuiModel) submit() tea.Model {
	line := strings.TrimSpace(m.input.Value())
	m.input.Reset()
	m.input.SetHeight(1)
	if line == "" {
		return m
	}
	// Save to input history (avoid duplicating last entry)
	if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != line {
		m.inputHistory = append(m.inputHistory, line)
		// Persist to history file
		if m.historyPath != "" {
			_ = appendTuiHistory(m.historyPath, line)
		}
	}
	m.historyIdx = -1
	m.reading = false
	m.requestStartTime = time.Now()
	// User messages must not use kind "text": assistant text streams are
	// coalesced with the previous text block while tokens arrive. If the user
	// prompt were a text block too, the next assistant response could be appended
	// to it, leaving stale render/scroll caches and breaking follow-to-bottom.
	m.forceRenderDirtyBlocks()
	m.blocks = append(m.blocks, block{kind: "user", content: "You: " + line})
	// A new block only shifts line offsets; earlier blocks render the same,
	// so their caches stay valid.
	m.invalidateTotalLines()
	// A new prompt should always jump back to the live bottom, even if the user
	// had scrolled up in the previous answer.
	m.atBottom = true
	m.scrollLine = 0
	m.clampScroll()
	if m.submitResult != nil {
		m.submitResult <- line
	}
	return m
}

func (m *TuiModel) capInputHeight() {
	// Use the textarea's own line count, which accounts for both hard newlines
	// and soft-wraps. Manual newline+width math was off whenever a logical
	// line was long enough to wrap on its own, causing lines to disappear.
	lines := m.input.LineCount()
	if lines < 1 {
		lines = 1
	}
	if lines > 10 {
		lines = 10
	}
	m.input.SetHeight(lines)
	// SetHeight changes viewport.Height but does not call the textarea's
	// unexported repositionView(). If a previous Update scrolled the viewport
	// down (because the old height was too small to show the cursor), those
	// lines are now hidden above the viewport. Sending PageUp scrolls the
	// viewport back to the top; the trailing repositionView in Update then
	// brings the cursor back into view if content exceeds the new height.
	m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyPgUp})
}

// ─── Block handling ───────────────────────────────────────────────────────
