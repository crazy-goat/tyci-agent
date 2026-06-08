package display

import "strings"

func (m TuiModel) visibleModalRenderBufferSnapshot() RenderBuffer {
	layout := m.subagentModalLayout()
	rb := newRenderBuffer(layout.contentHeight)
	allLines := strings.Split(m.subagentModalContent.String(), "\n")
	totalLines := len(allLines)

	visibleStart := 0
	if totalLines > layout.contentHeight {
		visibleStart = totalLines - layout.contentHeight - m.subagentModalScroll
		if visibleStart < 0 {
			visibleStart = 0
		}
	}
	visibleEnd := visibleStart + layout.contentHeight
	if visibleEnd > totalLines {
		visibleEnd = totalLines
	}

	y := layout.contentTop
	for i := visibleStart; i < visibleEnd && y <= layout.contentBottom; i++ {
		line := allLines[i]
		if len(line) > layout.popupWidth-4 {
			line = line[:layout.popupWidth-4]
		}
		rb.Add(line, "modal", -1, i, y)
		y++
	}
	for y <= layout.contentBottom {
		rb.Add("", "modal-empty", -1, -1, y)
		y++
	}
	return rb
}
