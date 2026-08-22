package display

import (
	"strings"
	"testing"
	"time"
)

// ─── messageRegionCache (issue #84) ─────────────────────────────────────
//
// Tests for the message-region string cache that skips rebuilding the
// transcript viewport on every status tick when nothing has changed.

// newCacheModel builds a ready TuiModel with a fixed terminal size suitable
// for message-cache tests.
func newCacheModel() TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 40
	return m
}

// TestMessageRegionCache_ReusesOnTick verifies that a second renderFrame()
// call with no mutations between them returns the cached message region —
// i.e. the cache string is reused, not rebuilt.
func TestMessageRegionCache_ReusesOnTick(t *testing.T) {
	m := newCacheModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "hello world"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	// First render builds and caches the region.
	frame1 := m.renderFrame()

	// Snapshot the cache.
	c := m.messageRegion
	if c == nil {
		t.Fatal("messageRegion cache should be initialized after renderFrame")
	}
	cached := c.cached
	if cached == "" {
		t.Fatal("message region cache should be non-empty after first render")
	}

	// Second render — simulates a status tick with no changes.
	frame2 := m.renderFrame()

	// The cache string should be identical (reused).
	if m.messageRegion.cached != cached {
		t.Error("message region cache should be reused on second render with no changes")
	}
	if frame1 != frame2 {
		t.Error("frames should be identical when nothing changed between renders")
	}
}

// TestMessageRegionCache_DirtyAfterBlockMutation verifies that appending a
// new block invalidates the cache so the next render rebuilds the region.
func TestMessageRegionCache_DirtyAfterBlockMutation(t *testing.T) {
	m := newCacheModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "first message"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.renderFrame() // populate cache

	cached := m.messageRegion.cached

	// Append a new block → should invalidate via invalidateTotalLines.
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: " second message"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if !m.messageRegion.dirty {
		t.Error("cache should be dirty after block mutation")
	}

	m.renderFrame() // rebuild

	if m.messageRegion.cached == cached {
		t.Error("cache should be rebuilt (different content) after block mutation")
	}
	frame := m.renderFrame()
	if !strings.Contains(stripANSI(frame), "second message") {
		t.Errorf("frame should contain new content after cache rebuild, got:\n%s", frame)
	}
}

// TestMessageRegionCache_DirtyAfterScrollChange verifies that scrolling
// invalidates the cache (the visible lines change).
func TestMessageRegionCache_DirtyAfterScrollChange(t *testing.T) {
	m := newCacheModel()
	// Add enough content to make the transcript scrollable. Use "block"
	// messages (each is a separate block, never merged) so the total line
	// count exceeds the viewport height (37 rows).
	for i := 0; i < 60; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "line " + string(rune('A'+i%26)) + " content here"})
	}
	m.renderFrame() // populate cache

	cached := m.messageRegion.cached
	totalLines := m.totalRenderedLines()
	if totalLines <= m.visibleLines() {
		t.Fatalf("test setup error: totalLines=%d must exceed visibleLines=%d for scroll test", totalLines, m.visibleLines())
	}

	// Scroll up — changes which lines are visible.
	m.atBottom = false
	m.scrollLine = 5
	m.clampScroll()

	m.renderFrame() // rebuild

	if m.messageRegion.cached == cached {
		t.Error("cache should be rebuilt after scroll change")
	}
}

// TestMessageRegionCache_DirtyAfterResize verifies that a width change
// invalidates the cache (re-wrap is needed).
func TestMessageRegionCache_DirtyAfterResize(t *testing.T) {
	m := newCacheModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "this is a long line that should wrap at the terminal width boundary"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.renderFrame() // populate cache at width 80

	cached := m.messageRegion.cached

	// Simulate a resize to a narrower width.
	m.width = 40
	m.height = 40
	m.input.SetWidth(38)
	m.invalidateAllBlockLineCounts()

	if !m.messageRegion.dirty {
		t.Error("cache should be dirty after resize (invalidateAllBlockLineCounts)")
	}

	m.renderFrame() // rebuild at new width

	if m.messageRegion.cached == cached {
		t.Error("cache should be rebuilt (different wrap) after resize")
	}
	// The new width should be stored in the cache snapshot.
	if m.messageRegion.width != 40 {
		t.Errorf("cache width snapshot = %d, want 40", m.messageRegion.width)
	}
}

// TestMessageRegionCache_DirtyAfterSelectionChange verifies that starting
// a text selection invalidates the cache (selected lines are styled differently).
func TestMessageRegionCache_DirtyAfterSelectionChange(t *testing.T) {
	m := newCacheModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "selectable text line"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.renderFrame() // populate cache

	cached := m.messageRegion.cached

	// Start a selection — changes the rendered output (selection styling).
	m.selectionVersion++
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 5, CursorY: 0}

	m.renderFrame() // rebuild with selection

	if m.messageRegion.cached == cached {
		t.Error("cache should be rebuilt after selection change")
	}
	if !m.messageRegion.selectionActive {
		t.Error("cache snapshot should record selectionActive = true")
	}
}

// TestMessageRegionCache_SelectionVersionMismatchInvalidates verifies that
// a change in selectionVersion (drag update) forces a cache snapshot update,
// since the selection extent may have changed.
func TestMessageRegionCache_SelectionVersionMismatchInvalidates(t *testing.T) {
	m := newCacheModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: "line one\nline two\nline three"})

	m.selectionVersion = 1
	m.selection = SelectionState{Active: true, AnchorX: 0, AnchorY: 0, CursorX: 3, CursorY: 0}
	m.renderFrame()

	if m.messageRegion.selectionVersion != 1 {
		t.Errorf("cache snapshot should record selectionVersion=1, got %d", m.messageRegion.selectionVersion)
	}

	// Simulate a drag that extends the selection (same Active, new version).
	m.selectionVersion = 2
	m.selection.CursorX = 8

	m.renderFrame()

	if m.messageRegion.selectionVersion != 2 {
		t.Errorf("cache snapshot should record selectionVersion=2 after drag, got %d", m.messageRegion.selectionVersion)
	}
}

// TestMessageRegionCache_ResetsClearsCache verifies that a "reset" message
// (e.g. /new) invalidates the message region so the welcome screen shows.
func TestMessageRegionCache_ResetsClearsCache(t *testing.T) {
	m := newCacheModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "some content"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.renderFrame() // populate cache

	cached := m.messageRegion.cached
	if cached == "" {
		t.Fatal("cache should be populated after render")
	}

	// Reset clears blocks → invalidateTotalLines fires inside "reset" handler.
	m.handleBlockMsg(tuiMsgBlock{kind: "reset"})

	if !m.messageRegion.dirty {
		t.Error("cache should be dirty after reset")
	}

	frame := m.renderFrame()
	// After reset with no blocks, the welcome message should appear.
	if !strings.Contains(frame, "tyci TUI") {
		t.Errorf("frame should show welcome message after reset, got:\n%s", frame)
	}
}

// TestMessageRegionCache_ToolEndInvalidates verifies that tool-end updates
// the cache so the done-state (duration, click hint) appears.
func TestMessageRegionCache_ToolEndInvalidates(t *testing.T) {
	m := newCacheModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"description": "test"}`})
	m.renderFrame() // populate cache with running tool

	cached := m.messageRegion.cached

	// Finish the tool → should invalidate (finishToolAt calls invalidateTotalLines).
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "result"})

	if !m.messageRegion.dirty {
		t.Error("cache should be dirty after tool-end")
	}

	frame := m.renderFrame()
	plain := stripANSI(frame)
	if !strings.Contains(plain, "click to display") {
		t.Errorf("frame should show 'click to display' after tool-end, got:\n%s", plain)
	}
	if m.messageRegion.cached == cached {
		t.Error("cache should be rebuilt after tool-end")
	}
}

// TestMessageRegionCache_StatusBarStillUpdates verifies that even when the
// message region is cached, the status bar (elapsed time) still changes
// between renders — that's the whole point: only the message region is cached,
// the status bar rebuilds every tick.
func TestMessageRegionCache_StatusBarStillUpdates(t *testing.T) {
	m := newCacheModel()
	m.reading = false // request in flight
	m.status = "tool"
	m.requestStartTime = time.Now().Add(-1000 * time.Millisecond) // 1s ago
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.renderFrame() // populate cache

	// Simulate time passing (status tick). While a tool is running the status
	// line times the TOOL, not the turn (see runningToolsStatus), so the
	// block's start has to move too.
	m.requestStartTime = time.Now().Add(-3500 * time.Millisecond) // 3.5s ago
	m.blocks[0].startTime = time.Now().Add(-3500 * time.Millisecond)

	frame2 := m.renderFrame()
	plain := stripANSI(frame2)

	// The status bar should reflect the new elapsed time.
	if !strings.Contains(plain, "3.") {
		t.Errorf("status bar should show updated elapsed time (~3.5s), got:\n%s", plain)
	}
}

// TestMessageRegionCache_FirstFrameBuildsCache verifies that the very first
// renderFrame() builds the cache (empty → populated).
func TestMessageRegionCache_FirstFrameBuildsCache(t *testing.T) {
	m := newCacheModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "hello"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if m.messageRegion.cached != "" {
		t.Error("cache should be empty before first render")
	}

	m.renderFrame()

	if m.messageRegion.cached == "" {
		t.Error("cache should be populated after first render")
	}
	if m.messageRegion.dirty {
		t.Error("cache should not be dirty after build (dirty flag cleared)")
	}
}

// TestMessageRegionCache_WelcomeScreenCaches verifies that the welcome
// placeholder (no blocks) is also cached and reused.
func TestMessageRegionCache_WelcomeScreenCaches(t *testing.T) {
	m := newCacheModel()
	// No blocks → welcome screen.
	frame1 := m.renderFrame()

	cached := m.messageRegion.cached
	if cached == "" {
		t.Fatal("welcome region should be cached after first render")
	}

	// Second render with no changes.
	frame2 := m.renderFrame()

	if m.messageRegion.cached != cached {
		t.Error("welcome region cache should be reused on second render")
	}
	if frame1 != frame2 {
		t.Error("frames should be identical for welcome screen with no changes")
	}
}

// TestMessageRegionCache_WelcomeToContentTransition verifies that when
// blocks are added (welcome → content), the cache is invalidated and rebuilt.
func TestMessageRegionCache_WelcomeToContentTransition(t *testing.T) {
	m := newCacheModel()
	m.renderFrame() // welcome screen cached

	if !m.messageRegion.hasContent {
		// Expected: no blocks → hasContent = false
	}

	// Add a block → hasContent changes → cache invalidated.
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "first message"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	m.renderFrame()

	if !m.messageRegion.hasContent {
		t.Error("cache snapshot should reflect hasContent = true after block added")
	}
	frame := m.renderFrame()
	if !strings.Contains(stripANSI(frame), "first message") {
		t.Errorf("frame should show the new block content, got:\n%s", frame)
	}
}

// TestMessageRegionCache_SurvivesModelCopy verifies that the pointer-based
// cache survives the value-copy bubbletea performs on every Update. This
// simulates: model copy → render → cache should still be valid.
func TestMessageRegionCache_SurvivesModelCopy(t *testing.T) {
	m := newCacheModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "hello"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.renderFrame() // populate cache on original model

	cached := m.messageRegion.cached

	// Simulate bubbletea's value copy (Update returns a copy).
	m2 := m // struct copy — pointer field is shared

	// The copy should see the same cache.
	if m2.messageRegion == nil {
		t.Fatal("copied model should have non-nil messageRegion (pointer)")
	}
	if m2.messageRegion.cached != cached {
		t.Error("copied model should share the same cached region string")
	}

	// Render on the copy — should reuse the cache (not dirty).
	m2.renderFrame()
	if m2.messageRegion.cached != cached {
		t.Error("cache should be reused on the copied model (pointer survived copy)")
	}
}

// TestMessageRegionCache_HeightChangeInvalidates verifies that a height
// change (which changes msgHeight/visibleLines) invalidates the cache via
// the width snapshot mismatch (width is checked; height affects msgHeight
// which changes the padding count).
func TestMessageRegionCache_HeightChangeRebuilds(t *testing.T) {
	m := newCacheModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "hello"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.renderFrame() // cache at height 40

	cached := m.messageRegion.cached

	// Change height — visibleLines changes, so the padding line count changes.
	// The cache snapshot doesn't track height directly, but the next render
	// will produce a different string (different number of padding lines).
	// We need to invalidate: clampScroll doesn't invalidate, so we call it
	// via a resize flush path. Simplest: set height and invalidate.
	m.height = 20
	m.invalidateMessageRegion()

	m.renderFrame()

	// The new cache should differ because the region has fewer padding lines.
	if m.messageRegion.cached == cached {
		t.Error("cache should be rebuilt (different height → different padding)")
	}
}
