package display

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// busPaneHeight is the number of lines of the preview pane under the Bus
// list: a separator, a header line and busPaneTextLines lines of text.
const (
	busPaneHeight    = 7
	busPaneTextLines = busPaneHeight - 2
)

// TuiBusRow is one message in the sidebar Bus tab. Text is already redacted
// by the caller. State is "sent", "received" or "failed".
type TuiBusRow struct {
	Seq   uint64
	At    time.Time
	From  string
	To    string // "*" for a broadcast
	Kind  string
	Text  string
	State string
}

// sidebarBusRows returns the messages of the Bus tab, newest first. The
// index of a row is the cursor position of its message.
func (m TuiModel) sidebarBusRows() []TuiBusRow {
	if m.busLister == nil {
		return nil
	}
	return m.busLister()
}

// busStateEmoji is the status column of a Bus row. The emoji is two cells wide.
func busStateEmoji(state string) string {
	switch state {
	case "received":
		return "📥"
	case "failed":
		return "❌"
	default:
		return "📤"
	}
}

// busRowText is the text of a Bus row without the cut: "<emoji> <from> -> <to>: <text>".
// A line break in the text becomes a space, so the row stays one line.
func busRowText(r TuiBusRow) string {
	text := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(r.Text)
	return busStateEmoji(r.State) + " " + r.From + " -> " + r.To + ": " + text
}

// busListLines returns one line per message, cut to width. The selected
// message is highlighted.
func (m TuiModel) busListLines(width int) []string {
	if m.busLister == nil {
		return []string{"", "  Bus messages are not wired up in this build."}
	}
	rows := m.sidebarBusRows()
	if len(rows) == 0 {
		return []string{"", "  No bus messages yet."}
	}
	out := make([]string, 0, len(rows))
	selected := m.busCursor(rows)
	for i, r := range rows {
		line := fillWidth(cutDots(busRowText(r), width), width)
		if i == selected {
			line = rowStyle(true).Render(line)
		}
		out = append(out, line)
	}
	return out
}

// busBodyLines returns the list from the scroll position on, in the lines the
// list can use, then the preview pane at the bottom when it is open. The
// list is padded, so the pane stays at the bottom of the sidebar.
func (m TuiModel) busBodyLines(layout sidebarLayoutT, width int) []string {
	list := m.busListLines(width)
	scroll := m.sidebarVisibleScroll(layout)
	listHeight := m.sidebarListHeight(layout.contentHeight)
	out := make([]string, 0, layout.contentHeight)
	for i := 0; i < listHeight; i++ {
		if j := scroll + i; j < len(list) {
			out = append(out, list[j])
		} else {
			out = append(out, "")
		}
	}
	if m.sidebarBusPane {
		out = append(out, m.busPaneLines(width)...)
	}
	return out
}

// busCursor returns the index of the selected message in rows. The selection
// is the Seq of the message, so a newer row does not move it. Without a
// selected Seq, or when that message is no longer listed, the cursor index is used.
func (m TuiModel) busCursor(rows []TuiBusRow) int {
	if m.sidebarBusSeq != 0 {
		for i, r := range rows {
			if r.Seq == m.sidebarBusSeq {
				return i
			}
		}
	}
	return m.sidebarCursor
}

// sidebarBusResolve sets the cursor to the selected message, so a key acts on
// the row the user sees.
func (m *TuiModel) sidebarBusResolve() {
	m.sidebarCursor = m.busCursor(m.sidebarBusRows())
}

// sidebarBusRemember stores the Seq of the message under the cursor as the selection.
func (m *TuiModel) sidebarBusRemember() {
	m.sidebarBusSeq = 0
	if r, ok := m.busSelected(); ok {
		m.sidebarBusSeq = r.Seq
	}
}

// busSelected returns the message under the cursor.
func (m TuiModel) busSelected() (TuiBusRow, bool) {
	rows := m.sidebarBusRows()
	i := m.busCursor(rows)
	if i < 0 || i >= len(rows) {
		return TuiBusRow{}, false
	}
	return rows[i], true
}

// busPaneScrollMax is the last first line the pane can show at width.
func (m TuiModel) busPaneScrollMax(width int) int {
	r, ok := m.busSelected()
	if !ok {
		return 0
	}
	return max(0, len(wrapCells(r.Text, width))-busPaneTextLines)
}

// busPaneLines returns the busPaneHeight lines of the preview pane at width:
// a separator, the header of the selected message, and its full text from
// sidebarBusScroll on, wrapped at width.
func (m TuiModel) busPaneLines(width int) []string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	out := make([]string, 0, busPaneHeight)
	out = append(out, dim.Render(strings.Repeat("─", width)))
	r, ok := m.busSelected()
	if !ok {
		for len(out) < busPaneHeight {
			out = append(out, fillWidth("", width))
		}
		return out
	}
	out = append(out, dim.Render(fillWidth(cutDots(busStateEmoji(r.State)+" "+r.From+" -> "+r.To+" "+r.Kind, width), width)))
	text := wrapCells(r.Text, width)
	scroll := min(m.sidebarBusScroll, m.busPaneScrollMax(width))
	for i := 0; i < busPaneTextLines; i++ {
		line := ""
		if j := scroll + i; j < len(text) {
			line = text[j]
		}
		out = append(out, fillWidth(line, width))
	}
	return out
}

// cutDots cuts s to width cells and ends a cut line with "...". A line that
// fits is returned as it is. A cut never splits a grapheme cluster.
func cutDots(s string, width int) string {
	const dots = "..."
	if width <= 0 {
		return ""
	}
	if cellWidth(s) <= width {
		return s
	}
	if width <= len(dots) {
		return dots[:width]
	}
	avail := width - len(dots)
	var out strings.Builder
	used := 0
	for _, t := range layoutTokens(s) {
		if used+t.width > avail {
			break
		}
		out.WriteString(t.value)
		used += t.width
	}
	return out.String() + dots
}

// wrapCells splits s at line breaks and then at width cells. It never splits
// a grapheme cluster. An empty s gives one empty line.
func wrapCells(s string, width int) []string {
	var lines []string
	for _, src := range strings.Split(s, "\n") {
		var cur strings.Builder
		used := 0
		for _, t := range layoutTokens(src) {
			if used > 0 && used+t.width > width {
				lines = append(lines, cur.String())
				cur.Reset()
				used = 0
			}
			cur.WriteString(t.value)
			used += t.width
		}
		lines = append(lines, cur.String())
	}
	return lines
}
