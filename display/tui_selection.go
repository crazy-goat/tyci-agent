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
	AnchorY   int
	CursorY   int
	PressX    int
	PressY    int
}

var selectionLineStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("63")).
	Foreground(lipgloss.Color("230"))

var selectionFlashLineStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("220")).
	Foreground(lipgloss.Color("232")).
	Bold(true)

func (m TuiModel) transcriptY(y int) bool {
	return y >= 0 && y < m.visibleLines()
}

func (m TuiModel) clampTranscriptY(y int) int {
	if y < 0 {
		return 0
	}
	maxY := m.visibleLines() - 1
	if maxY < 0 {
		return 0
	}
	if y > maxY {
		return maxY
	}
	return y
}

func (m TuiModel) selectedLineRange() (int, int, bool) {
	if !m.selection.Active {
		return 0, 0, false
	}
	start, end := m.selection.AnchorY, m.selection.CursorY
	if start > end {
		start, end = end, start
	}
	start = m.clampTranscriptY(start)
	end = m.clampTranscriptY(end)
	return start, end, true
}

func (m TuiModel) isLineSelected(y int) bool {
	start, end, ok := m.selectedLineRange()
	return ok && y >= start && y <= end
}

func (m TuiModel) renderSelectableLine(line string, y int) string {
	if !m.isLineSelected(y) {
		return line
	}
	// Existing rendered lines often already contain ANSI styles from markdown,
	// tool blocks, or lipgloss. Applying a background around those styles only
	// highlights padding/gaps in many terminals. For selected lines, render the
	// plain text version so the selection background covers the whole content.
	plain := plainLine(line)
	width := m.width
	if width < 1 {
		width = lipgloss.Width(plain)
	}
	style := selectionLineStyle
	if m.selectionFlash {
		style = selectionFlashLineStyle
	}
	return style.Width(width).MaxWidth(width).Render(plain)
}

func (m TuiModel) selectedText() string {
	start, end, ok := m.selectedLineRange()
	if !ok {
		return ""
	}
	rb := m.renderBuffer
	if len(rb.Lines) == 0 {
		rb = m.visibleRenderBufferSnapshot()
	}
	parts := make([]string, 0, end-start+1)
	for _, line := range rb.Lines {
		if line.Y < start || line.Y > end {
			continue
		}
		if line.SourceKind == "empty" {
			parts = append(parts, "")
			continue
		}
		parts = append(parts, line.PlainText)
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
