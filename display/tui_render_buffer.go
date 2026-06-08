package display

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// RenderLine describes one visible terminal line produced by the TUI renderer.
// Text is the styled line used for display; PlainText is the same line without
// ANSI styling, suitable for copying and hit-testing.
type RenderLine struct {
	Text       string
	PlainText  string
	SourceKind string
	BlockIndex int
	SourceLine int
	Y          int
}

// RenderBuffer stores metadata for the currently visible transcript area.
type RenderBuffer struct {
	Lines []RenderLine
}

func newRenderBuffer(capacity int) RenderBuffer {
	if capacity < 0 {
		capacity = 0
	}
	return RenderBuffer{Lines: make([]RenderLine, 0, capacity)}
}

func (rb *RenderBuffer) Add(text, sourceKind string, blockIndex, sourceLine, y int) {
	rb.Lines = append(rb.Lines, RenderLine{
		Text:       text,
		PlainText:  plainLine(text),
		SourceKind: sourceKind,
		BlockIndex: blockIndex,
		SourceLine: sourceLine,
		Y:          y,
	})
}

func plainLine(s string) string {
	return strings.TrimSuffix(ansi.Strip(s), clearLine)
}

type flatRenderLine struct {
	Text       string
	SourceKind string
	BlockIndex int
	SourceLine int
}

func (m TuiModel) visibleRenderBufferSnapshot() RenderBuffer {
	msgHeight := m.visibleLines()
	rb := newRenderBuffer(msgHeight)
	allLines := m.buildFlatRenderLines()
	for _, line := range allLines {
		if m.width > 0 && lipgloss.Width(line.Text) > m.width {
			wrapped := wrapText(line.Text, m.width, 0)
			for _, wl := range strings.Split(wrapped, "\n") {
				if len(rb.Lines) >= msgHeight {
					break
				}
				wl = strings.TrimSuffix(wl, clearLine)
				rb.Add(wl, line.SourceKind, line.BlockIndex, line.SourceLine, len(rb.Lines))
			}
		} else {
			rb.Add(line.Text, line.SourceKind, line.BlockIndex, line.SourceLine, len(rb.Lines))
		}
	}
	for len(rb.Lines) < msgHeight {
		rb.Add("", "empty", -1, -1, len(rb.Lines))
	}
	return rb
}

// buildFlatRenderLines builds a flat line array covering the visible viewport only.
// It skips blocks entirely before the viewport and stops after the viewport is filled,
// avoiding O(n) processing of all blocks on every render when scrolled to a specific position.
// cachedTotalLines is set to the total number of lines (for scroll calculations).
func (m *TuiModel) buildFlatRenderLines() []flatRenderLine {
	msgHeight := m.visibleLines()
	if msgHeight <= 0 {
		return nil
	}

	totalLines := m.totalRenderedLines()
	if totalLines <= msgHeight {
		// Everything fits in viewport — build full list (existing fast path)
		return m.buildAllFlatRenderLines()
	}

	// Calculate visible line range in the total line space
	var startLine int
	if m.scrollLine > 0 {
		startLine = totalLines - msgHeight - m.scrollLine
		if startLine < 0 {
			startLine = 0
		}
	} else {
		// At bottom — show the last msgHeight lines
		startLine = totalLines - msgHeight
		if startLine < 0 {
			startLine = 0
		}
	}
	endLine := startLine + msgHeight - 1

	return m.buildFlatRenderLinesInRange(startLine, endLine)
}

// buildAllFlatRenderLines builds the flat line array for ALL blocks.
func (m *TuiModel) buildAllFlatRenderLines() []flatRenderLine {
	var all []flatRenderLine
	for i := range m.blocks {
		lines := m.getBlockLines(i, false)
		if len(lines) == 0 {
			continue
		}
		for j, line := range lines {
			all = append(all, flatRenderLine{
				Text:       line,
				SourceKind: m.blocks[i].kind,
				BlockIndex: i,
				SourceLine: j,
			})
		}
		if i+1 < len(m.blocks) && !(m.blocks[i+1].kind == "tool" && m.blocks[i].kind == "tool") {
			all = append(all, flatRenderLine{Text: "", SourceKind: "spacer", BlockIndex: -1, SourceLine: -1})
		}
	}
	if len(all) > 0 && all[len(all)-1].Text == "" {
		all = all[:len(all)-1]
	}
	return all
}

// buildFlatRenderLinesInRange returns flat lines for blocks that overlap with
// the inclusive line range [startLine, endLine] in the total line space.
func (m *TuiModel) buildFlatRenderLinesInRange(startLine, endLine int) []flatRenderLine {
	var visible []flatRenderLine
	acc := 0 // accumulated line count so far

	for i := range m.blocks {
		lines := m.getBlockLines(i, false)
		blockLines := len(lines)
		if blockLines == 0 {
			continue
		}

		// Account for the spacer after this block (except between consecutive tool blocks)
		hasSpacer := i+1 < len(m.blocks) && !(m.blocks[i+1].kind == "tool" && m.blocks[i].kind == "tool")
		blockEnd := acc + blockLines
		if hasSpacer {
			blockEnd++ // spacer line
		}

		// Does this block overlap with the visible range?
		if blockEnd > startLine && acc <= endLine {
			// Include lines from this block
			for j, line := range lines {
				lineIdx := acc + j
				if lineIdx >= startLine && lineIdx <= endLine {
					visible = append(visible, flatRenderLine{
						Text:       line,
						SourceKind: m.blocks[i].kind,
						BlockIndex: i,
						SourceLine: j,
					})
				}
			}
			// Include spacer if it's in range
			if hasSpacer {
				spacerIdx := acc + blockLines
				if spacerIdx >= startLine && spacerIdx <= endLine {
					visible = append(visible, flatRenderLine{
						Text: "", SourceKind: "spacer", BlockIndex: -1, SourceLine: -1,
					})
				}
			}
		}

		// Advance accumulator
		acc = blockEnd

		// Stop once we've passed the viewport
		if acc > endLine {
			break
		}
	}
	return visible
}
