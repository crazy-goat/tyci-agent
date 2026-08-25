package display

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

func (m TuiModel) submit() tea.Model {
	line := strings.TrimSpace(m.input.Value())
	m.input.Reset()
	m.input.SetHeight(1)
	// The line has been sent, so there is nothing left to complete.
	m.closeFileComplete()
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
	// If the agent is busy, route the line to the pending-message queue
	// (issue #88) instead of submitting it through the results channel.
	// The line will be drained by the agent loop at the next safe point
	// and delivered to the model as a user RichMessage in a single
	// runOnce. The transcript is NOT updated yet — only the queue panel
	// shows the pending line. The "You: …" block is appended on drain
	// (when the message is actually sent to the model), so the user's
	// view of the conversation stays aligned with what the model has
	// seen so far.
	if !m.reading {
		m.queueItems = append(m.queueItems, line)
		m.invalidateTotalLines()
		if m.queue != nil {
			if !enqueueOrStatus(m.queue, line, &m.statusMessage) {
				// Channel was full — drop the snapshot item so the
				// panel doesn't show a message the model will never
				// see.
				m.queueItems = m.queueItems[:len(m.queueItems)-1]
			}
		}
		return m
	}
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

// enqueueOrStatus attempts a non-blocking send on ch. On success it returns
// true. On a full channel it sets *status to queueFullStatusMessage and
// returns false (issue #88 acceptance criteria #8: never block the event
// loop, never silently swallow).
func enqueueOrStatus(ch chan string, line string, status *string) bool {
	select {
	case ch <- line:
		return true
	default:
		if status != nil {
			*status = queueFullStatusMessage
		}
		return false
	}
}

func (m *TuiModel) capInputHeight() {
	// Height must match the number of DISPLAY rows the textarea renders, which
	// includes soft word-wrap: a long logical line breaks into several rows at
	// the widget's wrap width, and the input has to grow to fit them all or
	// wrapped lines clip. The bubbles textarea's LineCount() only counts hard
	// newlines (len(m.value)), so we re-count wrapped rows at the actual wrap
	// width (see inputWrapWidth / inputWrappedLineCount). Beyond the 10-row
	// cap the textarea's internal viewport scrolls.
	lines := inputWrappedLineCount(m.input.Value(), m.inputWrapWidth())
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

// insertNewline inserts a hard newline at the cursor. Common to Alt+Enter and
// Ctrl+N/Ctrl+J so the idle and busy handlers can't drift (they both used to
// copy the same height/scroll dance inline). The height is pre-set BEFORE the
// textarea's Update so its internal repositionView already targets the new
// viewport: without it, repositionView runs at the old (smaller) height,
// decides the new cursor line is out of view, scrolls down, and then
// capInputHeight can't undo that scroll — the first line disappears.
func (m TuiModel) insertNewline() TuiModel {
	newH := inputWrappedLineCount(m.input.Value(), m.inputWrapWidth()) + 1
	if newH < 1 {
		newH = 1
	} else if newH > 10 {
		newH = 10
	}
	m.input.SetHeight(newH)
	m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.capInputHeight()
	return m
}

// inputWrapWidth is the column width the textarea soft-wraps its input at.
// renderFrame / handleResize SetWidth the widget to max(10, colWidth-2), and
// textarea.SetWidth then reserves horizontal space for the prompt before the
// text-wrap width (m.width on the widget). The sidebar narrows the main
// column: an open sidebar means the input is actually drawn at
// mainColumnWidth()-2, so sizing the height from the full width would
// under-count wrap rows and the field would be too short.
func (m TuiModel) inputWrapWidth() int {
	col := m.width
	if m.sidebarActive {
		col = m.mainColumnWidth()
	}
	outer := max(10, col-2) // the value passed to textarea.SetWidth
	// The input uses the default prompt "▍ " (2 cols) and has ShowLineNumbers
	// disabled, so SetWidth reserves 2 inner columns. Ensure at least one text
	// column remains.
	const reserved = 2
	minInner := reserved + 1
	inner := max(outer, minInner) - reserved
	if inner < 1 {
		inner = 1
	}
	return inner
}

// inputWrappedLineCount returns how many display rows the textarea will render
// for value at the given soft-wrap width. It mirrors the bubbles textarea's
// word-wrap (bubbles/textarea.wrap): break at space boundaries, and if a
// single word is wider than the wrap width, hard-break it character by
// character. Keeping this in sync with the widget means capInputHeight's
// height always matches what View() paints.
func inputWrappedLineCount(value string, width int) int {
	if width < 1 {
		width = 1
	}
	total := 0
	for _, logical := range strings.Split(value, "\n") {
		total += wrappedRows([]rune(logical), width)
	}
	return max(1, total)
}

func wrappedRows(r []rune, width int) int {
	if len(r) == 0 {
		return 1
	}
	var (
		lines  = [][]rune{{}}
		word   = []rune{}
		row    int
		spaces int
	)
	for _, cp := range r {
		if cp == ' ' || cp == '\t' {
			spaces++
		} else {
			word = append(word, cp)
		}
		if spaces > 0 {
			if runewidth.StringWidth(string(lines[row]))+
				runewidth.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
				spaces, word = 0, nil
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
				spaces, word = 0, nil
			}
			continue
		}
		// Accumulating an unbroken word: break it if it outgrows the width.
		lastCharW := runewidth.RuneWidth(word[len(word)-1])
		if runewidth.StringWidth(string(word))+lastCharW > width {
			if len(lines[row]) > 0 {
				row++
				lines = append(lines, []rune{})
			}
			lines[row] = append(lines[row], word...)
			word = nil
		}
	}
	if runewidth.StringWidth(string(lines[row]))+
		runewidth.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		spaces++
		lines[row+1] = append(lines[row+1], repeatSpaces(spaces)...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], repeatSpaces(spaces)...)
	}
	return len(lines)
}

func repeatSpaces(n int) []rune {
	return []rune(strings.Repeat(" ", n))
}

// ─── Block handling ───────────────────────────────────────────────────────
