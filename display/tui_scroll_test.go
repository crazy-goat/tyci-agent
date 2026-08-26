package display

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestTuiModel_SubmitCreatesUserBlockAndResetsScroll(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 40
	m.height = 10
	m.scrollLine = 5
	m.input.SetValue("next prompt")

	updated := m.submit().(TuiModel)

	if updated.scrollLine != 0 {
		t.Fatalf("expected submit to reset scroll to bottom, got %d", updated.scrollLine)
	}
	if len(updated.blocks) != 1 {
		t.Fatalf("expected one block, got %d", len(updated.blocks))
	}
	if updated.blocks[0].kind != "user" {
		t.Fatalf("expected user block, got %q", updated.blocks[0].kind)
	}
}

func TestTuiModel_AssistantTextDoesNotAppendToSubmittedUserPrompt(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 40
	m.height = 10
	m.input.SetValue("prompt")
	m = m.submit().(TuiModel)

	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "assistant answer"})

	if len(m.blocks) != 2 {
		t.Fatalf("expected user and assistant blocks, got %d", len(m.blocks))
	}
	if m.blocks[0].kind != "user" || !strings.Contains(m.blocks[0].content, "prompt") {
		t.Fatalf("unexpected first block: kind=%q content=%q", m.blocks[0].kind, m.blocks[0].content)
	}
	if m.blocks[1].kind != "text" || m.blocks[1].content != "assistant answer" {
		t.Fatalf("unexpected assistant block: kind=%q content=%q", m.blocks[1].kind, m.blocks[1].content)
	}
}

func TestTuiModel_KeyEndRestoresAutoScrollAfterPrompt(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 30
	m.height = 8
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: strings.Repeat("old content ", 40)})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.scrollLine = 10
	m.input.SetValue("prompt")
	m = m.submit().(TuiModel)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = model.(TuiModel)
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: strings.Repeat("new content ", 40)})

	if m.scrollLine != 0 {
		t.Fatalf("expected streaming to stay at bottom after End, got scrollLine=%d", m.scrollLine)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "new content") {
		t.Fatalf("expected view to include new streamed content, got:\n%s", view)
	}
}

func TestTuiModel_ViewAtBottomShowsTailOfLongStreamingBlock(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	m.height = 8 // visible message lines = 5
	m.atBottom = true

	for i := 1; i <= 12; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: fmt.Sprintf("line-%02d\n", i)})
	}

	view := stripANSI(m.View())
	if strings.Contains(view, "line-01") {
		t.Fatalf("bottom view showed start of long block instead of tail:\n%s", view)
	}
	if !strings.Contains(view, "line-12") {
		t.Fatalf("bottom view did not show newest line:\n%s", view)
	}
}

// TestHandleResize_InvalidatesWidthCachesImmediately is F14's regression
// test. handleResize used to set m.width immediately but leave the
// width-keyed block caches (cachedLineCount/cachedLines, streamWraps) alone
// until handleResizeFlush fired ~100ms later. In that window, renderWidth()
// already reported the new, narrower width, but a block's cached lines were
// still wrapped for the old, wider one — so a line rendered at width 80
// could come back 78 columns wide while renderWidth()==30, which
// buildViewportRows' overlong-line safety net then re-wrapped as plain
// text, shredding any ANSI in it (a bold/glamour-styled line, not a raw
// one, so the corruption is visible, not just short).
func TestHandleResize_InvalidatesWidthCachesImmediately(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24

	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "**" + strings.Repeat("word ", 20) + "**"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.renderFrame() // populate cachedLineCount/cachedLines at width 80

	if lc := m.blocks[0].cachedLineCount; lc == 0 {
		t.Fatal("expected the block to have a cached line count after rendering at width 80")
	}

	model, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 24})
	m2 := model.(TuiModel)
	if m2.width != 30 {
		t.Fatalf("expected m.width to update immediately, got %d", m2.width)
	}
	if !m2.resizePending {
		t.Fatal("expected the flush debounce to still be pending (the expensive repaint stays debounced)")
	}

	// Simulate the ~100ms window before resizeFlushMsg fires: the cache must
	// already be invalidated, without waiting for handleResizeFlush.
	if lc := m2.blocks[0].cachedLineCount; lc != 0 {
		t.Fatalf("expected handleResize to invalidate the block's cached line count immediately, still has %d", lc)
	}

	renderWidth := m2.renderWidth()
	if renderWidth != 30 {
		t.Fatalf("expected renderWidth() == 30 immediately after resize, got %d", renderWidth)
	}
	for i, l := range m2.buildAllFlatRenderLines() {
		if w := lipgloss.Width(l.Text); w > renderWidth {
			t.Fatalf("line %d is %d cols wide mid-resize, wider than renderWidth() = %d: %q", i, w, renderWidth, l.Text)
		}
	}
}
