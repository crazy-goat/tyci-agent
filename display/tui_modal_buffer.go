package display

func (m TuiModel) visibleModalRenderBufferSnapshot() RenderBuffer {
	layout := m.subagentModalLayout()
	rb := newRenderBuffer(layout.contentHeight)
	// Wrapped, not truncated — see subagentModalWrappedLines.
	allLines := m.subagentModalWrappedLines()
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
		// allLines is already wrapped to layout.popupWidth-4, nothing left to cut.
		rb.Add(allLines[i], "modal", -1, i, y)
		y++
	}
	for y <= layout.contentBottom {
		rb.Add("", "modal-empty", -1, -1, y)
		y++
	}
	return rb
}
