package display

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// RenderLine describes one visible terminal line produced by the TUI renderer.
// Text is the styled line used for display; PlainText is the same line without
// ANSI styling, suitable for copying and hit-testing. PlainText is filled
// lazily — use plain() instead of reading the field, so the ANSI stripping
// cost is only paid when a selection is actually copied, not on every render.
type RenderLine struct {
	Text       string
	PlainText  string
	SourceKind string
	BlockIndex int
	SourceLine int
	Y          int
}

// plain returns the ANSI-stripped form of the line, computing it on demand.
func (l RenderLine) plain() string {
	if l.PlainText != "" || l.Text == "" {
		return l.PlainText
	}
	return plainLine(l.Text)
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

// visibleRenderBufferSnapshot maps screen rows to their source block, for
// selection and mouse hit-testing. It must agree with what was drawn row for
// row, which is why both go through buildViewportRows.
func (m TuiModel) visibleRenderBufferSnapshot() RenderBuffer {
	msgHeight := m.messageRegionHeight()
	rb := newRenderBuffer(msgHeight)
	for _, row := range m.buildViewportRows(msgHeight) {
		rb.Add(row.Text, row.SourceKind, row.BlockIndex, row.SourceLine, len(rb.Lines))
	}
	return rb
}

// buildFlatRenderLines builds a flat line array covering the visible viewport only.
// It skips blocks entirely before the viewport and stops after the viewport is filled,
// avoiding O(n) processing of all blocks on every render when scrolled to a specific position.
// cachedTotalLines is set to the total number of lines (for scroll calculations).
// msgHeight is passed in rather than recomputed so the window is always sized
// against the height the region will really be drawn at (see
// messageRegionHeight).
//
// anchorTop reports that the window reaches the transcript's first line while
// the person is scrolled up. It matters because a window of msgHeight LINES
// can expand past msgHeight ROWS once lines wrap, and the caller then has to
// decide which end of the overflow to drop: normally the newest end must
// survive, but at the top of the transcript the oldest must.
func (m *TuiModel) buildFlatRenderLines(msgHeight int) (lines []flatRenderLine, anchorTop bool) {
	if msgHeight <= 0 {
		return nil, false
	}

	totalLines := m.totalRenderedLines()
	if totalLines <= msgHeight {
		// Everything fits in viewport — build full list (existing fast path)
		return m.buildAllFlatRenderLines(), !m.atBottom
	}

	// Calculate visible line range in the total line space
	var startLine int
	if m.scrollLine > 0 {
		startLine = totalLines - msgHeight - m.scrollLine
		if startLine < 0 {
			startLine = 0
		}
	} else {
		// At bottom — show the last msgHeight lines. The painter scrolls the
		// message region in hardware (scroll region + SU) so per-line shifts are
		// cheap, so we pin exactly to the bottom for genuinely smooth scrolling.
		startLine = totalLines - msgHeight
		if startLine < 0 {
			startLine = 0
		}
	}
	endLine := startLine + msgHeight - 1

	return m.buildFlatRenderLinesInRange(startLine, endLine), startLine == 0 && !m.atBottom
}

// renderRow is one row of the transcript viewport, after wrapping.
type renderRow struct {
	Text       string
	SourceKind string
	BlockIndex int
	SourceLine int
}

// buildViewportRows returns EXACTLY msgHeight rows for the transcript
// viewport: the window expanded to screen rows, anchored, and padded.
//
// The wrapping step is the reason this exists. The transcript's line space
// counts unwrapped lines — that is what totalRenderedLines and scrollLine are
// measured in — but the region's budget is screen rows, and a line wider than
// the terminal takes several of them. Slicing the window in the line space
// and then stopping at msgHeight rows silently dropped the tail: with any
// wrapped text on screen, the newest lines never reached the bottom of the
// viewport, which looked like the transcript getting stuck part-way down.
//
// Wrapping can only ever add rows, never remove them, so a window of
// msgHeight lines always yields at least msgHeight rows — which means
// anchoring can be done here without asking for more lines.
//
// Shared by the drawing path and the selection/hit-test snapshot. They used to
// carry their own copies of this loop, so any fix had to be made twice.
func (m *TuiModel) buildViewportRows(msgHeight int) []renderRow {
	if msgHeight < 1 {
		msgHeight = 1
	}
	lines, anchorTop := m.buildFlatRenderLines(msgHeight)

	rows := make([]renderRow, 0, msgHeight+8)
	for _, line := range lines {
		if m.width > 0 && lipgloss.Width(line.Text) > m.width {
			for _, wl := range strings.Split(wrapText(line.Text, m.width, 0), "\n") {
				rows = append(rows, renderRow{
					Text:       strings.TrimSuffix(wl, clearLine),
					SourceKind: line.SourceKind,
					BlockIndex: line.BlockIndex,
					SourceLine: line.SourceLine,
				})
			}
			continue
		}
		rows = append(rows, renderRow{
			Text:       line.Text,
			SourceKind: line.SourceKind,
			BlockIndex: line.BlockIndex,
			SourceLine: line.SourceLine,
		})
	}

	if len(rows) > msgHeight {
		if anchorTop {
			rows = rows[:msgHeight]
		} else {
			rows = rows[len(rows)-msgHeight:]
		}
	}
	// Structural padding below the transcript, NOT content blank lines —
	// marked with a distinct SourceKind so selection skips it and no block
	// ever folds it into its own cached lines.
	for len(rows) < msgHeight {
		rows = append(rows, renderRow{Text: "", SourceKind: "viewport-pad", BlockIndex: -1, SourceLine: -1})
	}
	return rows
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
