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
	totalLines := len(allLines)
	var startIdx int
	if totalLines > msgHeight {
		startIdx = totalLines - msgHeight - m.scrollLine
		if startIdx < 0 {
			startIdx = 0
		}
	}
	rendered := 0
	for i := startIdx; i < totalLines && rendered < msgHeight; i++ {
		line := allLines[i]
		if m.width > 0 && lipgloss.Width(line.Text) > m.width {
			wrapped := wrapText(line.Text, m.width, 0)
			for _, wl := range strings.Split(wrapped, "\n") {
				if rendered >= msgHeight {
					break
				}
				wl = strings.TrimSuffix(wl, clearLine)
				rb.Add(wl, line.SourceKind, line.BlockIndex, line.SourceLine, rendered)
				rendered++
			}
		} else {
			rb.Add(line.Text, line.SourceKind, line.BlockIndex, line.SourceLine, rendered)
			rendered++
		}
	}
	for rendered < msgHeight {
		rb.Add("", "empty", -1, -1, rendered)
		rendered++
	}
	return rb
}

func (m *TuiModel) buildFlatRenderLines() []flatRenderLine {
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
