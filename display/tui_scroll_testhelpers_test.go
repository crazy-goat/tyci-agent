package display

// blockAtVisibleLine returns the index of the block drawn on screen row visY
// (0 = first message row), or -1 for a spacer or a row outside the transcript.
// Production code uses visibleLine, which returns the whole line. Only the
// tests need the block index alone.
func (m *TuiModel) blockAtVisibleLine(visY int) int {
	if line, ok := m.visibleLine(visY); ok {
		return line.BlockIndex
	}
	return -1
}
