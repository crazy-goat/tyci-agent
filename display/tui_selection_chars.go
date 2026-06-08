package display

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

func selectableLineWidth(m TuiModel, line string) int {
	width := m.width
	if m.subagentModalActive {
		layout := m.subagentModalLayout()
		width = layout.popupWidth - 4
	}
	if width < 1 {
		width = lipgloss.Width(line)
	}
	return width
}

func screenXToSelectionX(m TuiModel, rawX int) int {
	if m.subagentModalActive {
		layout := m.subagentModalLayout()
		x := rawX - layout.left - 2 // border + content padding
		if x < 0 {
			return 0
		}
		return x
	}
	if rawX < 0 {
		return 0
	}
	return rawX
}

func cellIndexToByteIndex(s string, cell int) int {
	if cell <= 0 {
		return 0
	}
	plain := ansi.Strip(s)
	w := 0
	for i, r := range plain {
		rw := runewidth.RuneWidth(r)
		if rw <= 0 {
			rw = 0
		}
		if w+rw > cell {
			return i
		}
		w += rw
		if w == cell {
			return i + len(string(r))
		}
	}
	return len(plain)
}

func cutCells(s string, from, to int) string {
	plain := ansi.Strip(s)
	if from < 0 {
		from = 0
	}
	if to < from {
		to = from
	}
	start := cellIndexToByteIndex(plain, from)
	end := cellIndexToByteIndex(plain, to)
	if start > len(plain) {
		start = len(plain)
	}
	if end > len(plain) {
		end = len(plain)
	}
	if end < start {
		end = start
	}
	return plain[start:end]
}

func renderSelectedSpan(plain string, from, to, lineWidth int, style lipgloss.Style) string {
	if lineWidth < lipgloss.Width(plain) {
		lineWidth = lipgloss.Width(plain)
	}
	if from < 0 {
		from = 0
	}
	if to < from {
		to = from
	}
	if to > lineWidth {
		to = lineWidth
	}
	prefix := cutCells(plain, 0, from)
	selected := cutCells(plain, from, to)
	selectedWidth := lipgloss.Width(selected)
	if to > lipgloss.Width(plain) {
		selected += strings.Repeat(" ", to-max(from, lipgloss.Width(plain)))
		selectedWidth = lipgloss.Width(selected)
	}
	if selectedWidth == 0 && to > from {
		selected = strings.Repeat(" ", to-from)
	}
	suffix := cutCells(plain, to, lipgloss.Width(plain))
	return prefix + style.Render(selected) + suffix
}
