package display

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// totalRenderedLines returns the total number of terminal lines all blocks would
// occupy when rendered, including separator blank lines between blocks.
// Uses cached value when available; call invalidateTotalLines() to force recompute.
func (m *TuiModel) totalRenderedLines() int {
	if m.cachedTotalLines >= 0 {
		return m.cachedTotalLines
	}
	total := 0
	for i, b := range m.blocks {
		lc := b.cachedLineCount
		if lc == 0 {
			// Try to get lines (renders if needed)
			lines := m.getBlockLines(i, false)
			if lines == nil {
				continue
			}
			lc = len(lines)
		}
		total += lc
		// Separator blank line between blocks (skip between consecutive tool blocks)
		if i+1 < len(m.blocks) && m.blocks[i+1].kind == "tool" && b.kind == "tool" {
			continue
		}
		total++
	}
	if total > 0 && len(m.blocks) > 0 {
		// Remove trailing blank line
		total--
	}
	m.cachedTotalLines = total
	return total
}

// invalidateTotalLines forces recomputation of cached line counts.
func (m *TuiModel) invalidateTotalLines() {
	m.cachedTotalLines = -1
}

// invalidateAllBlockLineCounts clears per-block line counts and total line cache.
// Called on resize since wrap width changes.
func (m *TuiModel) invalidateAllBlockLineCounts() {
	for i := range m.blocks {
		m.blocks[i].cachedLineCount = 0
		m.blocks[i].cachedLines = nil
	}
	m.streamWraps = make(map[int]*streamWrap)
	m.mdCacheRendered = make(map[int]string)
	m.cachedTotalLines = -1
}

// agentBusy reports whether the agent is actively producing output.
func (m *TuiModel) agentBusy() bool {
	switch m.status {
	case "thinking", "responding", "tool":
		return true
	}
	return false
}

// jumpScrollStart quantizes the viewport start line to a multiple of a quarter
// of the window height, rounding up from minStart (the exact bottom-pinned
// start). Between jumps the returned value is constant while content grows, so
// visible rows do not shift and the renderer's positional line diff can skip
// them. A quarter-screen step keeps the jumps small enough to feel smooth while
// still batching several appends per shift.
//
// Only the ticker-based standard renderer uses this; the custom painter scrolls
// the message region smoothly one line at a time via a hardware scroll region
// (see paintRegion), so it does not quantize.
func jumpScrollStart(minStart, msgHeight int) int {
	jump := msgHeight / 4
	if jump < 1 {
		jump = 1
	}
	return (minStart + jump - 1) / jump * jump
}

// clampScroll ensures scrollLine is within valid range.
func (m *TuiModel) clampScroll() {
	if m.atBottom {
		m.scrollLine = 0
	}
	if m.scrollLine == 0 {
		return // auto-scrolling, no need to compute max
	}
	maxLine := m.totalRenderedLines()
	if m.scrollLine > maxLine {
		m.scrollLine = maxLine
	}
}

// visibleLines returns the number of terminal lines available for message display.
func (m TuiModel) visibleLines() int {
	return max(1, m.height-m.input.Height()-2)
}

func (m *TuiModel) blockAtVisibleLine(visY int) int {
	// Use the cached render buffer from the last View() call for fast lookup.
	// The buffer is rebuilt on every render and maps screen Y → block index.
	if visY >= 0 && visY < len(m.renderBuffer.Lines) {
		return m.renderBuffer.Lines[visY].BlockIndex
	}
	// Fallback when View() hasn't been called yet (e.g., in tests):
	// compute the block index by iterating with accumulated line counts.
	if visY < 0 {
		return -1
	}
	msgHeight := m.visibleLines()
	totalLines := m.totalRenderedLines()
	var startLine int
	if totalLines > msgHeight {
		startLine = totalLines - msgHeight - m.scrollLine
		if startLine < 0 {
			startLine = 0
		}
	}
	targetLine := startLine + visY
	if targetLine < 0 || targetLine >= totalLines {
		return -1
	}
	acc := 0
	for i, b := range m.blocks {
		lc := b.cachedLineCount
		if lc == 0 {
			lines := m.getBlockLines(i, false)
			if lines == nil {
				continue
			}
			lc = len(lines)
		}
		blockEnd := acc + lc
		if targetLine < blockEnd {
			return i
		}
		// Account for spacer
		if i+1 < len(m.blocks) && !(m.blocks[i+1].kind == "tool" && m.blocks[i].kind == "tool") {
			blockEnd++
		}
		acc = blockEnd
	}
	return -1
}

// ─── View ─────────────────────────────────────────────────────────────────
