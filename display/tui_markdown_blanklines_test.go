package display

import (
	"strings"
	"testing"
	"time"
)

// TestBlankCollapse verifies that runs of 2+ blank lines collapse to a single
// blank line while non-blank content is preserved exactly.
func TestBlankCollapse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"multiple blanks", "a\n\n\n\nb", "a\n\nb"},
		{"single blank kept", "a\n\nb", "a\n\nb"},
		{"no blanks", "a\nb", "a\nb"},
		{"empty", "", ""},
		{"whitespace-only lines treated as blank", "a\n   \n\t\nb", "a\n   \nb"},
		{"leading blanks collapse", "\n\n\na", "\na"},
		{"trailing blanks collapse", "a\n\n\n", "a\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := collapseMarkdownBlankLines(c.in)
			if got != c.want {
				t.Errorf("collapseMarkdownBlankLines(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestBlankCollapsePreservesANSI ensures styled (colored) content lines are
// passed through byte-for-byte; only blank lines are dropped.
func TestBlankCollapsePreservesANSI(t *testing.T) {
	styled := "\x1b[38;5;150mhello\x1b[0m"
	in := styled + "\n\n\n" + styled
	got := collapseMarkdownBlankLines(in)
	want := styled + "\n\n" + styled
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !strings.Contains(got, "\x1b[38;5;150m") {
		t.Error("ANSI color codes should be preserved")
	}
}

// TestBlankRenderWhitespaceContent verifies renderMarkdownWithCache does not
// panic or infinite-loop for whitespace-only content. A watchdog goroutine
// fails the test if the call hangs.
func TestBlankRenderWhitespaceContent(t *testing.T) {
	done := make(chan string, 1)
	go func() {
		done <- renderMarkdownWithCache("   ", false, 80)
	}()
	select {
	case out := <-done:
		// Whatever glamour returns must at least not blow up. We don't assert
		// exact output because glamour's handling of pure whitespace is
		// version-dependent; empty or collapsed output is acceptable.
		_ = out
	case <-time.After(5 * time.Second):
		t.Fatal("renderMarkdownWithCache hung on whitespace-only content")
	}
}

// TestBlankEmptyBlockCache is an integration test: a "text" block with empty
// content, once force-rendered, must have cachedLines == []string{} (non-nil,
// empty) and cachedLineCount == 0 — never [""] / 1.
func TestBlankEmptyBlockCache(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 40

	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: ""})

	// Locate the text block.
	idx := -1
	for i := range m.blocks {
		if m.blocks[i].kind == "text" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no text block created")
	}

	// Mark dirty and force-render.
	m.blocks[idx].dirty = true
	m.dirtyBlocks[idx] = true
	m.forceRenderDirtyBlocks()

	b := m.blocks[idx]
	if b.cachedLines == nil {
		t.Fatalf("cachedLines should be non-nil empty slice, got nil")
	}
	if len(b.cachedLines) != 0 {
		t.Errorf("cachedLines should be empty, got %v", b.cachedLines)
	}
	if b.cachedLineCount != 0 {
		t.Errorf("cachedLineCount = %d, want 0", b.cachedLineCount)
	}
}
