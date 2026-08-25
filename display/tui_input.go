package display

import (
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
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

// inputMaxHeight is the tallest the prompt input grows to before the widget's
// own internal viewport starts scrolling vertically.
const inputMaxHeight = 10

func (m TuiModel) inputEffectiveColumn() int {
	if m.sidebarActive {
		return m.mainColumnWidth()
	}
	return m.width
}

// setInputWidth is the single place that converts the current layout (full
// width, or the narrower main column with the sidebar open) into the widget's
// wrap width. Every path that changes geometry — terminal resize, sidebar
// open/close — must call it so the widget and the height computation agree.
// The widget reserves its prompt/gutter before the text-wrap width, so after
// this the wrap width is available as m.input.Width().
func (m *TuiModel) setInputWidth() {
	m.input.SetWidth(max(10, m.inputEffectiveColumn()-2))
}

func (m *TuiModel) capInputHeight() {
	// Height must match the number of DISPLAY rows the textarea renders, which
	// includes soft word-wrap: a long logical line breaks into several rows at
	// the widget's wrap width, and the input has to grow to fit them all or
	// wrapped lines clip. The bubbles textarea's LineCount() only counts hard
	// newlines (len(m.value)), so we re-count wrapped rows with inputWrapped,
	// a verbatim mirror of the widget's own word-wrap. The wrap width comes
	// from the widget itself (m.input.Width()), which after setInputWidth
	// already subtracts the prompt reservation; sizing from m.width would
	// miss that and over/under-count. Beyond inputMaxHeight the widget's
	// internal viewport scrolls.
	//
	// Crucially we do NOT inject a synthetic scroll key (e.g. KeyPgUp) after
	// SetHeight: the widget's View() clamps an over-long YOffset back to the
	// bottom (SetContent→GotoBottom when past the last line) and repositionView
	// on the next Update keeps the cursor in view, both against the widget's
	// own geometry. A forced top/bottom scroll from here would yank the
	// viewport away from the cursor and hide the very lines the user is
	// typing.
	width := m.input.Width()
	if width < 1 {
		width = 1
	}
	lines := inputWrapped(m.input.Value(), width)
	if lines < 1 {
		lines = 1
	}
	if lines > inputMaxHeight {
		lines = inputMaxHeight
	}
	m.input.SetHeight(lines)
	// Render now (the message region height uses m.input.Height()), which also
	// lets the widget clamp the viewport to valid bounds for the new height.
	_ = m.input.View()
}

// insertNewline inserts a hard newline at the cursor. Common to Alt+Enter and
// Ctrl+N/Ctrl+J so the idle and busy handlers can't drift (they both used to
// copy the same height/scroll dance inline).
//
// The newline is inserted FIRST (the widget's Update splits the line at the
// cursor, which changes how subsequent text wraps), then height is computed
// from the post-insertion content. Pre-sizing "wrapped rows + 1" was wrong
// whenever the cursor sat in the middle of a long line: the split rearranges
// the wrap boundaries, so the pre-computed height didn't match what the widget
// now renders and the field was one row short or one row too tall.
func (m TuiModel) insertNewline() TuiModel {
	m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.capInputHeight()
	return m
}

// inputWrapped returns how many display rows the textarea renders for value at
// the given soft-wrap width. It is a verbatim mirror of bubbles' textarea.wrap
// (github.com/charmbracelet/bubbles textarea): unicode.IsSpace is the word
// separator (not just ASCII space/tab), and widths come from rivo/uniseg plus
// mattn/go-runewidth for the last-rune hard-break. Because it is byte-for-byte
// the same algorithm the widget uses, capInputHeight's height always matches
// what View() paints, including for tabs, U+3000, emoji and other wide runes.
func inputWrapped(value string, width int) int {
	if width < 1 {
		width = 1
	}
	total := 0
	for _, logical := range strings.Split(value, "\n") {
		total += len(wrapInput([]rune(logical), width))
	}
	if total < 1 {
		total = 1
	}
	return total
}

func wrapInput(runes []rune, width int) [][]rune {
	var (
		lines  = [][]rune{{}}
		word   = []rune{}
		row    int
		spaces int
	)
	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}
		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+
				uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatInputSpaces(spaces)...)
				spaces = 0
				word = nil
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatInputSpaces(spaces)...)
				spaces = 0
				word = nil
			}
		} else {
			lastCharLen := runewidth.RuneWidth(word[len(word)-1])
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}
	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		spaces++
		lines[row+1] = append(lines[row+1], repeatInputSpaces(spaces)...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], repeatInputSpaces(spaces)...)
	}
	return lines
}

func repeatInputSpaces(n int) []rune {
	return []rune(strings.Repeat(" ", n))
}

// ─── Block handling ───────────────────────────────────────────────────────
