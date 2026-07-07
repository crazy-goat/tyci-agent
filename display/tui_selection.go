package display

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type SelectionState struct {
	Active    bool
	Dragging  bool
	Candidate bool
	AnchorX   int // selection cell, relative to selectable content
	AnchorY   int
	CursorX   int // selection cell, relative to selectable content
	CursorY   int
	PressX    int // raw screen cell, for click-vs-drag detection
	PressY    int
}

var selectionLineStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("63")).
	Foreground(lipgloss.Color("230"))

var selectionFlashLineStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("220")).
	Foreground(lipgloss.Color("232")).
	Bold(true)

func (m TuiModel) selectionYRange() (int, int) {
	if m.subagentModalActive {
		layout := m.subagentModalLayout()
		return layout.contentTop, layout.contentBottom
	}
	return 0, m.visibleLines() - 1
}

func (m TuiModel) transcriptY(y int) bool {
	start, end := m.selectionYRange()
	return y >= start && y <= end
}

func (m TuiModel) clampTranscriptY(y int) int {
	start, maxY := m.selectionYRange()
	if y < start {
		return start
	}
	if maxY < start {
		return start
	}
	if y > maxY {
		return maxY
	}
	return y
}

type selectionPoint struct {
	X int
	Y int
}

func (m TuiModel) normalizeSelection() (selectionPoint, selectionPoint, bool) {
	if !m.selection.Active {
		return selectionPoint{}, selectionPoint{}, false
	}
	start := selectionPoint{X: m.selection.AnchorX, Y: m.clampTranscriptY(m.selection.AnchorY)}
	end := selectionPoint{X: m.selection.CursorX, Y: m.clampTranscriptY(m.selection.CursorY)}
	if start.Y > end.Y || (start.Y == end.Y && start.X > end.X) {
		start, end = end, start
	}
	if start.X < 0 {
		start.X = 0
	}
	if end.X < 0 {
		end.X = 0
	}
	return start, end, true
}

func (m TuiModel) renderSelectableLine(line string, y int) string {
	start, end, ok := m.normalizeSelection()
	if !ok || y < start.Y || y > end.Y {
		return line
	}
	// Existing rendered lines often already contain ANSI styles from markdown,
	// tool blocks, or lipgloss. For selected spans, render the plain text version
	// so selection colors are predictable and copy text matches what is shown.
	plain := plainLine(line)
	lineWidth := selectableLineWidth(m, plain)
	style := selectionLineStyle
	if m.selectionFlash {
		style = selectionFlashLineStyle
	}

	from, to := 0, lipgloss.Width(plain)
	if y == start.Y {
		from = start.X
	}
	if y == end.Y {
		to = end.X
	}
	if start.Y != end.Y {
		if y == start.Y {
			to = lineWidth
		} else if y == end.Y {
			from = 0
			to = end.X
		} else {
			from = 0
			to = lineWidth
		}
	}
	if from < 0 {
		from = 0
	}
	if to < from {
		to = from
	}
	return renderSelectedSpan(plain, from, to, lineWidth, style)
}

func (m TuiModel) selectedText() string {
	start, end, ok := m.normalizeSelection()
	if !ok {
		return ""
	}
	rb := m.renderBuffer
	if m.subagentModalActive {
		rb = m.modalRenderBuffer
		if len(rb.Lines) == 0 {
			rb = m.visibleModalRenderBufferSnapshot()
		}
	} else if len(rb.Lines) == 0 {
		rb = m.visibleRenderBufferSnapshot()
	}
	parts := make([]string, 0, end.Y-start.Y+1)
	for _, line := range rb.Lines {
		if line.Y < start.Y || line.Y > end.Y {
			continue
		}
		if line.SourceKind == "empty" || line.SourceKind == "modal-empty" || line.SourceKind == "viewport-pad" {
			parts = append(parts, "")
			continue
		}
		plain := line.plain()
		from, to := 0, lipgloss.Width(plain)
		if line.Y == start.Y {
			from = start.X
		}
		if line.Y == end.Y {
			to = end.X
		}
		if start.Y != end.Y {
			if line.Y == start.Y {
				to = lipgloss.Width(plain)
			} else if line.Y == end.Y {
				from = 0
				to = end.X
			} else {
				from = 0
				to = lipgloss.Width(plain)
			}
		}
		parts = append(parts, strings.TrimRight(cutCells(plain, from, to), " \t"))
	}
	return strings.TrimRight(strings.Join(parts, "\n"), "\n")
}

func (m TuiModel) copyText(label, text string, flashSelection bool) TuiModel {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		m.statusMessage = "nothing to copy"
		return m
	}
	if err := copyToClipboard(text); err != nil {
		m.statusMessage = err.Error()
		return m
	}
	if flashSelection {
		m.selectionFlash = true
	}
	lines := strings.Count(text, "\n") + 1
	m.statusMessage = "copied " + label
	if lines > 1 {
		m.statusMessage = "copied " + label + " (" + strconv.Itoa(lines) + " lines)"
	}
	return m
}

func (m TuiModel) copySelection() TuiModel {
	return m.copyText("selection", m.selectedText(), true)
}

func (m TuiModel) clearSelection() TuiModel {
	m.selectionVersion++
	m.selection = SelectionState{}
	m.selectionFlash = false
	return m
}
