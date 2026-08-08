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
// Also invalidates the message region cache (issue #84): anything that
// changes block layout (add/change/remove/scroll) changes the transcript
// viewport, so the cached region string is stale.
func (m *TuiModel) invalidateTotalLines() {
	m.cachedTotalLines = -1
	m.invalidateMessageRegion()
}

// invalidateAllBlockLineCounts clears per-block line counts and total line cache.
// Called on resize since wrap width changes.
//
// Flushed blocks (rendered lines paged to the scrollback file) are left
// untouched: their cachedLineCount is still valid for the *old* width and the
// file holds the old-wrapping. They'll be paged back in and re-wrapped lazily
// by getBlockLines/ensureBlockResident the next time the viewport actually
// needs them, so a resize doesn't force a full history re-render.
func (m *TuiModel) invalidateAllBlockLineCounts() {
	for i := range m.blocks {
		if m.blocks[i].flushed {
			continue // stay paged out; re-wrapped lazily on view
		}
		m.blocks[i].cachedLineCount = 0
		m.blocks[i].cachedLines = nil
	}
	m.streamWraps = make(map[int]*streamWrap)
	m.mdCacheRendered = make(map[int]string)
	m.cachedTotalLines = -1
	m.invalidateMessageRegion()
	// Resident bytes changed: recount from the surviving resident blocks.
	m.scrollback.residentBytes = m.residentBlockBytes()
}

// agentBusy reports whether the agent is actively producing output.
func (m *TuiModel) agentBusy() bool {
	switch m.status {
	case "thinking", "responding", "tool":
		return true
	}
	return false
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

// visibleLines returns the number of terminal rows available for messages.
// The frame lays out as: 1 top-bar row + msgHeight message rows + 1 status
// row + m.input.Height() input rows = m.height total, so
// msgHeight = m.height - 2 - m.input.Height().
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
		// Account for spacer between non-consecutive-tool blocks.
		if i+1 < len(m.blocks) && !(m.blocks[i+1].kind == "tool" && b.kind == "tool") {
			// Spacer occupies exactly one line at blockEnd.
			if targetLine == blockEnd {
				return -1 // spacer line — not part of any block
			}
			blockEnd++
		}
		acc = blockEnd
	}
	return -1
}

// ─── View ─────────────────────────────────────────────────────────────────

// messageRegionCache holds the rendered message-region string (the transcript
// area between the top bar and the status bar) so the status tick doesn't
// rebuild it on every 250ms fire. Pointer-referenced by TuiModel so it
// survives the value-copy bubbletea performs on every Update, exactly like
// scrollback and painter. See issue #84.
//
// The cache uses two layers of invalidation:
//  1. A dirty flag set by invalidateTotalLines/invalidateAllBlockLineCounts
//     (covers block content/layout mutations and resize).
//  2. A state snapshot (scrollLine, atBottom, selectionVersion, width,
//     hasContent) compared on every lookup. This catches scroll and selection
//     changes that don't go through the dirty-flag path, without needing
//     invalidate calls scattered across 20+ event handlers.
type messageRegionCache struct {
	cached           string // the last rendered message region ("" = not built yet)
	dirty            bool   // true when block layout/content changed
	scrollLine       int    // scroll position when cache was built
	atBottom         bool   // atBottom when cache was built
	selectionVersion int    // selection version when cache was built
	selectionActive  bool   // selection active when cache was built
	selectionFlash   bool   // selection flash when cache was built
	width            int    // terminal width when cache was built
	hasContent       bool   // whether blocks existed when cache was built
}

// invalidateMessageRegion marks the message region cache as stale so the next
// renderFrame() rebuilds it. Call from every code path that mutates blocks,
// scroll position, width (resize → re-wrap), or selection state — anything
// that could change which lines appear in the transcript viewport.
func (m *TuiModel) invalidateMessageRegion() {
	if m.messageRegion == nil {
		return
	}
	m.messageRegion.dirty = true
}
