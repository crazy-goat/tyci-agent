package display

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// queuePanelMaxLines is the maximum number of pending user messages shown
// in the queue panel. The (5th+) line is replaced with "… and N more".
const queuePanelMaxLines = 4

// queueFullStatusMessage is shown to the user when the underlying channel
// is full and a submit is dropped (issue #88 acceptance criteria #8).
const queueFullStatusMessage = "queue full, try again"

// renderQueuePanel renders the pending-message panel that appears between
// the status bar and the input when the user has typed lines while the
// agent is busy. Returns an empty string when the queue is empty (zero
// height, no border) so the layout matches the queue-empty case exactly.
//
// Display rules (issue #88, #4):
//   - at most queuePanelMaxLines lines are shown
//   - if len > queuePanelMaxLines, the (queuePanelMaxLines+1)th line reads
//     "… and N more" with N = len(queue) - queuePanelMaxLines
//   - empty queue → empty string (panel hidden entirely)
//   - lines are styled dim with a leading "▸" glyph
//   - narrow terminals: each line is truncated to fit `width`
func (m TuiModel) renderQueuePanel(width int) string {
	if len(m.queueItems) == 0 {
		return ""
	}
	if width < 1 {
		width = 1
	}

	style := lipgloss.NewStyle().
		Width(width).MaxWidth(width).
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("243"))

	var b strings.Builder
	max := len(m.queueItems)
	if max > queuePanelMaxLines {
		max = queuePanelMaxLines
	}
	for i := 0; i < max; i++ {
		line := "▸ " + m.queueItems[i]
		b.WriteString(style.Render(truncateToWidth(line, width)))
		b.WriteString("\n")
	}
	if len(m.queueItems) > queuePanelMaxLines {
		more := len(m.queueItems) - queuePanelMaxLines
		moreLine := truncateToWidth("… and "+strconv.Itoa(more)+" more", width)
		b.WriteString(style.Render(moreLine))
		b.WriteString("\n")
	}
	return b.String()
}

// queuePanelHeight returns the number of terminal rows the queue panel
// would occupy when rendered at the given width. Used by the render path
// to shrink the message viewport.
func (m TuiModel) queuePanelHeight() int {
	n := len(m.queueItems)
	if n == 0 {
		return 0
	}
	if n > queuePanelMaxLines {
		return queuePanelMaxLines + 1
	}
	return n
}

// truncateToWidth truncates s to fit `width` terminal columns, appending
// "…" if truncation occurred. Width must be >= 1.
func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(s)
	for len(runes) > 1 {
		candidate := string(runes[:len(runes)-1]) + "…"
		if lipgloss.Width(candidate) <= width {
			return candidate
		}
		runes = runes[:len(runes)-1]
	}
	return "…"
}
