package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestSidebarOpen_WideTableRendersAtMainColumnWidth is the regression test
// for the "tables render broken while the sidebar is open" report.
//
// Failure mode being pinned: a finished markdown block was glamour-rendered
// and cached at the full terminal width even while the sidebar was open, but
// the only thing on screen is the narrowed main column. buildViewportRows'
// overlong-line safety net then hard-wrapped the ALREADY RENDERED markdown as
// plain text — splitting a table's box-drawing border rows mid-glyph and
// shredding the whole table. Resizing or re-toggling the sidebar "fixed" it,
// because invalidateAllBlockLineCounts forced a re-render that now happened
// under the narrowed shadow model.
//
// The fix is renderWidth(): caches are built at mainColumnWidth while the
// sidebar is open, so they hold exactly what the screen shows. The table
// below is deliberately wider than the main column but narrower than the
// full test terminal — a content-sized small table would fit either way and
// could not catch the old behaviour.
func TestSidebarOpen_WideTableRendersAtMainColumnWidth(t *testing.T) {
	m := newTestModelForSidebar()

	// Long cells so the rendered table spans well past the sidebar-open
	// main column (100*2/5+1 = 41 cols) yet fits the full 100-col terminal.
	row := func(a, b string) string { return "| " + a + " | " + b + " |\n" }
	table := "| Option | What it does |\n" +
		"|--------|--------------|\n" +
		row("renderWidth", "one source of truth for the width block lines are wrapped and cached at")
		row("mainColumnWidth", "the narrowed main conversation column while the sidebar is open")

	m.appendOrAppend("text", table)
	m.forceRenderDirtyBlocks() // block finishes at full width, sidebar closed

	fullLines := m.getBlockLines(0, false)
	if len(fullLines) == 0 {
		t.Fatal("no lines rendered for the table block")
	}

	m.openSidebar(sidebarTabTokens)
	mainW := m.mainColumnWidth()
	if mainW >= m.width {
		t.Fatalf("test premise broken: main column (%d) not narrower than terminal (%d)", mainW, m.width)
	}

	// The block's cache must now hold the narrow-width render — byte-for-byte
	// identical to glamouring the same content directly at mainColumnWidth.
	wantRendered := renderMarkdownWithCache(collapseRepeatedLines(table), false, mainW)
	if wantRendered == "" {
		t.Fatal("reference narrow render came back empty")
	}
	want := strings.Split(wantRendered, "\n")
	got := m.getBlockLines(0, false)
	if len(got) != len(want) {
		t.Fatalf("block re-rendered at wrong width: got %d lines, want %d (narrow render)", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d differs from the narrow-width render:\ngot:  %q\nwant: %q", i, got[i], want[i])
		}
	}

	// End-to-end through the shadow model renderFrame actually uses: no
	// flat line may exceed the main column's width. Under the old caching
	// the full-width glamour lines blew straight through this bound (and
	// were then plain-text re-wrapped into shredded fragments).
	mainM := m
	mainM.width = mainW
	for i, l := range mainM.buildAllFlatRenderLines() {
		if w := lipgloss.Width(l.Text); w > mainW {
			t.Errorf("flat line %d is %d cols, wider than the main column (%d): %q", i, w, mainW, l.Text)
		}
	}
}
