package display

import (
	"strings"
	"testing"
)

// The transcript viewport has one job at the bottom of the screen: show the
// newest line. These tests exist because it quietly stopped doing that in two
// independent ways — a height computed differently at different call sites,
// and a window sliced in unwrapped lines but drawn in screen rows.

func viewportModel(height, width int) TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = width
	m.height = height
	m.ready = true
	m.atBottom = true
	return m
}

// addBlocks appends n assistant blocks, the last one carrying a marker so a
// test can ask "did the newest line make it onto the screen".
func addBlocks(m *TuiModel, n int, content, lastContent string) {
	for i := 0; i < n; i++ {
		body := content
		if i == n-1 {
			body = lastContent
		}
		m.blocks = append(m.blocks, block{kind: "assistant", content: body})
	}
	m.cachedTotalLines = -1
	m.invalidateMessageRegion()
}

const newestMarker = "NEWEST-LINE"

func regionContainsNewest(m *TuiModel) bool {
	region := m.buildMessageRegion(m.messageRegionHeight())
	return strings.Contains(stripANSI(region), newestMarker)
}

func TestViewportPinsBottomWithPlainLines(t *testing.T) {
	m := viewportModel(30, 60)
	addBlocks(&m, 60, "plain line", newestMarker)

	if !regionContainsNewest(&m) {
		t.Error("newest line is off screen with no wrapping and no panels")
	}
}

// TestViewportPinsBottomWithWrappedLines is the main regression: the window is
// chosen in unwrapped lines, but each wrapped line eats several rows, so
// stopping at msgHeight rows used to drop the newest content off the bottom.
// It triggered on any wrapped text, which in practice is most of a session.
func TestViewportPinsBottomWithWrappedLines(t *testing.T) {
	m := viewportModel(30, 40)
	addBlocks(&m, 40, strings.Repeat("word ", 30), newestMarker)

	if !regionContainsNewest(&m) {
		t.Errorf("newest line is off screen with wrapped text (msgHeight=%d, totalLines=%d)",
			m.messageRegionHeight(), m.totalRenderedLines())
	}
}

// TestViewportPinsBottomWithPanelVisible covers the second cause: the window
// was sized against visibleLines() while the region was drawn at
// visibleLines() minus the panels, so the last panelHeight lines fell off.
func TestViewportPinsBottomWithPanelVisible(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*TuiModel)
	}{
		{"queue panel", func(m *TuiModel) { m.queueItems = []string{"a", "b", "c"} }},
		{"file completion popup", func(m *TuiModel) {
			m.fileCompleteActive = true
			m.fileCompleteItems = []string{"a.go", "b.go"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := viewportModel(30, 60)
			addBlocks(&m, 60, "plain line", newestMarker)
			tc.setup(&m)
			m.invalidateMessageRegion()

			if !regionContainsNewest(&m) {
				t.Errorf("newest line is off screen (msgHeight=%d, visibleLines=%d)",
					m.messageRegionHeight(), m.visibleLines())
			}
		})
	}
}

// TestViewportFillsEveryRow: the region is a fixed-height block, and the frame
// arithmetic depends on it drawing exactly the rows it was asked for.
func TestViewportFillsEveryRow(t *testing.T) {
	for _, tc := range []struct {
		name        string
		width       int
		blocks      int
		content     string
		queueItems  []string
		emptyScreen bool
	}{
		{name: "no content", width: 60, emptyScreen: true},
		{name: "less content than the screen", width: 60, blocks: 3, content: "short"},
		{name: "more content than the screen", width: 60, blocks: 80, content: "short"},
		{name: "wrapped content", width: 40, blocks: 40, content: strings.Repeat("word ", 30)},
		{name: "with a panel", width: 60, blocks: 80, content: "short", queueItems: []string{"a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := viewportModel(30, tc.width)
			if !tc.emptyScreen {
				addBlocks(&m, tc.blocks, tc.content, tc.content)
			}
			m.queueItems = tc.queueItems
			m.invalidateMessageRegion()

			msgHeight := m.messageRegionHeight()
			region := m.buildMessageRegion(msgHeight)
			if got := strings.Count(region, "\n") + 1; got != msgHeight {
				t.Fatalf("region drew %d rows, was asked for %d", got, msgHeight)
			}
		})
	}
}

// TestViewportTopIsReachable: anchoring to the newest end must not make the
// oldest line unreachable when scrolled all the way up.
func TestViewportTopIsReachable(t *testing.T) {
	m := viewportModel(30, 40)
	m.blocks = append(m.blocks, block{kind: "assistant", content: "OLDEST-LINE"})
	addBlocks(&m, 40, strings.Repeat("word ", 30), "last")

	m.atBottom = false
	m.scrollLine = m.totalRenderedLines() // scrolled far past the top
	m.clampScroll()
	m.invalidateMessageRegion()

	region := stripANSI(m.buildMessageRegion(m.messageRegionHeight()))
	if !strings.Contains(region, "OLDEST-LINE") {
		t.Error("the first line of the transcript cannot be reached by scrolling up")
	}
}

// TestRenderBufferMatchesWhatWasDrawn: selection and mouse hit-testing read
// the snapshot, so a row-for-row disagreement with the drawn region sends
// clicks to the wrong block.
func TestRenderBufferMatchesWhatWasDrawn(t *testing.T) {
	m := viewportModel(30, 40)
	addBlocks(&m, 40, strings.Repeat("word ", 30), newestMarker)
	m.queueItems = []string{"a", "b"}
	m.invalidateMessageRegion()

	msgHeight := m.messageRegionHeight()
	drawn := strings.Split(stripANSI(m.buildMessageRegion(msgHeight)), "\n")
	snapshot := m.visibleRenderBufferSnapshot()

	if len(snapshot.Lines) != len(drawn) {
		t.Fatalf("snapshot has %d rows, %d were drawn", len(snapshot.Lines), len(drawn))
	}
	for i := range drawn {
		want := strings.TrimSpace(stripANSI(snapshot.Lines[i].PlainText))
		got := strings.TrimSpace(drawn[i])
		// The drawn row carries the gutter prefix; a containment check is
		// enough to catch an off-by-N misalignment, which is the failure
		// mode that matters.
		if want != "" && !strings.Contains(got, want) {
			t.Fatalf("row %d: snapshot says %q, screen shows %q", i, want, got)
		}
	}
}

// TestMessageRegionCacheKeyIncludesHeight: the region is height-shaped, and
// the height changes with no content change at all — a panel appears, the
// input grows a line, the terminal is resized vertically at the same width.
func TestMessageRegionCacheKeyIncludesHeight(t *testing.T) {
	m := viewportModel(30, 60)
	addBlocks(&m, 80, "plain line", newestMarker)

	tall := m.messageRegionHeight()
	first := m.buildMessageRegionCached(tall)
	if got := strings.Count(first, "\n") + 1; got != tall {
		t.Fatalf("got %d rows, want %d", got, tall)
	}

	// A panel appears: same content, same width, smaller region.
	m.queueItems = []string{"a", "b", "c"}
	short := m.messageRegionHeight()
	if short == tall {
		t.Fatal("setup: the panel should have shrunk the region")
	}
	second := m.buildMessageRegionCached(short)
	if got := strings.Count(second, "\n") + 1; got != short {
		t.Fatalf("cache returned a %d-row region for a %d-row request", got, short)
	}
}
