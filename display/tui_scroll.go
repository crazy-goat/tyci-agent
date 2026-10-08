package display

func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

// totalRenderedLines returns the total number of terminal lines all blocks would
// occupy when rendered, including separator blank lines between blocks.
// Uses cached value when available; call invalidateTotalLines() to force recompute.
func (m *TuiModel) totalRenderedLines() int {
	if m.agentView != nil {
		return m.agentView.model.totalRenderedLines()
	}
	if m.cachedTotalLines >= 0 {
		return m.cachedTotalLines
	}
	total := 0
	for first := 0; first < len(m.blocks); {
		last := m.unitEnd(first)
		// Counts and flat lines must agree exactly (see unitLineCount): an
		// over-count here scrolls past the end of the transcript and every
		// viewport row comes back as padding, i.e. a blank screen.
		if n := m.unitLineCount(first, last); n > 0 {
			total += n
			// Separator blank line after the unit — the same rule the flat-line
			// builders use, via the same helper (see spacerAfter).
			if m.spacerAfter(last) {
				total++
			}
		}
		first = last + 1
	}
	// No trailing adjustment: spacerAfter already reports false for the last
	// block, so nothing was added past the end. Subtracting one here — as
	// this did while it counted a spacer after every block — now undercounts
	// by one and pins the viewport a line above the newest output.
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

// invalidateDirtyBlockWidthCaches is F14's immediate, cheap half of the
// resize fix: it clears the width-keyed streaming caches (streamWraps,
// mdStreamState, mdCacheRendered) and cachedLineCount/cachedLines for
// CURRENTLY-STREAMING (dirty) blocks only, right when handleResize fires —
// not the full invalidateAllBlockLineCounts sweep, and deliberately not
// cachedTotalLines.
//
// Why the narrower scope: an earlier version of this fix called the full
// invalidateAllBlockLineCounts() immediately on every resize event, which a
// review round measured at up to ~37x the per-event cost on a session with
// a few hundred blocks (1.3ms -> 49ms for a single Update+View). The
// expense was never the invalidation itself (clearing maps/counters is
// O(dirty blocks)) — it was invalidateTotalLines() setting cachedTotalLines
// = -1, which forces totalRenderedLines' NEXT call (View() runs right after
// every Update(), so that's immediate) to loop over every resident block
// and, for every FLUSHED one, see `flushedWidth != renderWidth()` (true for
// all of them the instant m.width changes) and page it back in from disk —
// on every single resize event during a drag, not once per gesture.
//
// The actual bug this exists to fix — a line cached at the old width
// emitted at the new renderWidth() and shredded by buildViewportRows'
// overlong-line safety net — only has a REAL, newly-worsened window on the
// block that's actively streaming right now (streamWrap.stableLines /
// mdStreamState's renderedPrefixLines are exactly the caches that can hold
// a partial, old-width wrap while new tokens keep arriving mid-resize).
// Finished/resident blocks sit unchanged until something re-renders them;
// item 51 didn't change that pre-existing, accepted ~100ms-and-self-heals
// window for them (see this item's own history), so leaving their caches
// alone until the debounced handleResizeFlush — which still runs the FULL
// invalidateAllBlockLineCounts exactly as before this fix — is the same
// trade this project already made, just no longer also applied per-event
// to the one thing that got measurably worse.
func (m *TuiModel) invalidateDirtyBlockWidthCaches() {
	if len(m.dirtyBlocks) == 0 {
		return
	}
	for idx := range m.dirtyBlocks {
		if idx < 0 || idx >= len(m.blocks) {
			continue
		}
		m.blocks[idx].cachedLineCount = 0
		m.blocks[idx].cachedLines = nil
		delete(m.streamWraps, idx)
		delete(m.mdStreamState, idx)
		delete(m.mdCacheRendered, idx)
	}
	// buildMessageRegionCached's key already includes m.width (which
	// changed immediately, unrelated to this fix), so it would rebuild
	// anyway — this is just explicit and matches invalidateAllBlockLineCounts'
	// own call, at negligible cost (a bool set).
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
	m.mdStreamState = make(map[int]*mdStreamState)
	m.mdCacheRendered = make(map[int]string)
	m.cachedTotalLines = -1
	m.invalidateMessageRegion()
	// Resident bytes changed: recount from the surviving resident blocks.
	m.scrollback.residentBytes = m.residentBlockBytes()
}

// scrollDown moves the viewport n lines towards the newest content, and
// restores follow-the-bottom when it gets there.
//
// The "when it gets there" is the whole reason this is a function. Three call
// sites (PgDn, Ctrl+Down, wheel-down) each tested `scrollLine < 0` before
// setting atBottom, so a scroll that landed EXACTLY on line 0 left atBottom
// false — and the wheel moves three lines at a time, so landing exactly on 0
// is the common case, not the edge case. The result looked like the transcript
// had frozen: with atBottom false and scrollLine 0 the region anchors its
// content to the top and pads below it, and new output stops being followed.
func (m *TuiModel) scrollDown(n int) {
	*m = m.clearSelection()
	m.scrollLine -= n
	if m.scrollLine <= 0 {
		m.scrollLine = 0
		m.atBottom = true
	}
}

// clampScroll ensures scrollLine is within valid range.
func (m *TuiModel) clampScroll() {
	if m.atBottom {
		m.scrollLine = 0
	}
	// scrollLine 0 means "showing the newest content", which is what atBottom
	// means; the renderer reads them together, so they must not disagree.
	if m.scrollLine <= 0 {
		m.scrollLine = 0
		m.atBottom = true
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

// messageRegionHeight is how many terminal rows the transcript viewport
// actually gets: visibleLines() minus the panels that render between the
// status bar and the input.
//
// This is the single source of truth on purpose. The height used to be
// recomputed at each call site, some subtracting the panels and some not, and
// every disagreement showed up as a visible defect: the viewport window sized
// against one number while the region drew against another, so the newest
// lines fell off the bottom; mouse hit-testing resolved rows to the wrong
// block; and the painter was told it could hardware-scroll rows that belonged
// to a panel. If a new panel is ever added between the status bar and the
// input, subtract it here and every consumer stays correct.
func (m TuiModel) messageRegionHeight() int {
	return max(1, m.visibleLines()-m.queuePanelHeight()-m.jobsPanelHeight()-m.fileCompleteHeight())
}

// visibleLine returns the transcript line drawn on screen row visY (0 = first
// message row). ok is false for a row outside the transcript.
func (m *TuiModel) visibleLine(visY int) (flatRenderLine, bool) {
	// Use the cached render buffer from the last View() call for fast lookup.
	// The buffer is rebuilt on every render and maps screen Y → source line.
	if visY >= 0 && visY < len(m.renderBuffer.Lines) {
		rl := m.renderBuffer.Lines[visY]
		return flatRenderLine{Text: rl.Text, SourceKind: rl.SourceKind, BlockIndex: rl.BlockIndex, SourceLine: rl.SourceLine}, true
	}
	// Fallback when View() hasn't been called yet (e.g., in tests): work the
	// window out from the line counts, the same way the renderer does.
	if visY < 0 {
		return flatRenderLine{}, false
	}
	msgHeight := m.messageRegionHeight()
	totalLines := m.totalRenderedLines()
	var startLine int
	if totalLines > msgHeight {
		startLine = max(0, totalLines-msgHeight-m.scrollLine)
	}
	targetLine := startLine + visY
	if targetLine < 0 || targetLine >= totalLines {
		return flatRenderLine{}, false
	}
	lines := m.buildFlatRenderLinesInRange(targetLine, targetLine)
	if len(lines) == 0 {
		return flatRenderLine{}, false
	}
	return lines[0], true
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
	msgHeight        int    // region height when cache was built
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
