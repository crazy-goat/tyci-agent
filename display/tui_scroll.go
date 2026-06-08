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
	m.streamingCache = make(map[int]string)
	m.mdCacheRendered = make(map[int]string)
	m.cachedTotalLines = -1
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
	// Build flat lines with block index tracking
	type lineInfo struct {
		blockIdx int
	}
	var allLines []lineInfo

	for blkIdx := range m.blocks {
		lines := m.getBlockLines(blkIdx, false)
		if len(lines) == 0 {
			continue
		}
		for range lines {
			allLines = append(allLines, lineInfo{blockIdx: blkIdx})
		}
		// Separator blank line (skip if next block is also a tool)
		if blkIdx+1 < len(m.blocks) && m.blocks[blkIdx+1].kind == "tool" && m.blocks[blkIdx].kind == "tool" {
			continue
		}
		allLines = append(allLines, lineInfo{blockIdx: -1})
	}
	if len(allLines) > 0 && allLines[len(allLines)-1].blockIdx == -1 {
		allLines = allLines[:len(allLines)-1]
	}

	totalLines := len(allLines)
	msgHeight := m.visibleLines()

	var startIdx int
	if totalLines <= msgHeight {
		startIdx = 0
	} else {
		startIdx = totalLines - msgHeight - m.scrollLine
		if startIdx < 0 {
			startIdx = 0
		}
	}

	idx := startIdx + visY
	if idx >= 0 && idx < totalLines {
		return allLines[idx].blockIdx
	}
	return -1
}

// ─── View ─────────────────────────────────────────────────────────────────
