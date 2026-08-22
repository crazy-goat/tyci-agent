package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func thinkingModel() TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 60
	m.height = 30
	m.ready = true
	m.atBottom = true
	return m
}

// TestThinkingStyleIsGrey pins the intent. Asserting on emitted ANSI would be
// unreliable here: lipgloss picks its colour profile from the terminal, and
// under `go test` there is none, so every style renders as plain text. The
// style's configuration is the part this package actually decides.
func TestThinkingStyleIsGrey(t *testing.T) {
	for name, style := range map[string]lipgloss.Style{"text": thinkingStyle, "gutter": thinkingBarStyle} {
		fg := style.GetForeground()
		if fg == nil {
			t.Fatalf("%s has no foreground colour configured", name)
		}
		adaptive, ok := fg.(lipgloss.AdaptiveColor)
		if !ok {
			t.Fatalf("%s: expected an adaptive colour so it stays readable on light and dark terminals, got %T", name, fg)
		}
		if adaptive.Light == "" || adaptive.Dark == "" {
			t.Fatalf("%s: both variants must be set: %+v", name, adaptive)
		}
	}
}

// TestThinkingKeepsItsGutter: selection measures the collapsed line's
// "│ thinking " prefix per source kind (see gutterLen), so losing it would
// misalign copied text — the same concern "tool"'s "┃ tool " prefix has.
func TestThinkingKeepsItsGutter(t *testing.T) {
	m := thinkingModel()
	m.blocks = append(m.blocks, block{kind: "thinking", content: "line one\nline two", thinkingSummary: "line one line two"})

	plain := ansi.Strip(m.renderBlock(0, m.blocks[0]))
	if !strings.HasPrefix(plain, "│ thinking ") {
		t.Fatalf("collapsed line should start with the bar+label gutter, got: %q", plain)
	}
	if got := gutterLen("thinking", plain); got != len([]rune("│ thinking ")) {
		t.Fatalf("gutterLen(\"thinking\", ...) = %d, want %d", got, len([]rune("│ thinking ")))
	}
}

// A thinking block used to render its full text inline through the same dim
// raw-text pipeline as a tool's dim output, and had to stay visually
// identical across three render paths (streaming, settled,
// forceRenderDirtyBlocks) to avoid flashing a different colour as it
// finished. It no longer renders inline at all — it always collapses to one
// summary line (renderThinkingBlock) — so that full-render parity no longer
// applies. What used to be TestThinkingIsGreyOnEveryPath,
// TestThinkingBlockUsesTheDimPipeline, TestThinkingLineCountIsPreserved and
// TestThinkingLooksTheSameWhileStreaming covered that old behaviour; the
// collapsed behaviour (stable summary across streaming deltas, correct line
// count, the click-to-display affordance) is covered by
// tui_thinking_collapsed_test.go instead.

// TestTextBlockIsNotDimmed: the answer must keep its own markdown styling.
func TestTextBlockIsNotDimmed(t *testing.T) {
	m := thinkingModel()
	m.blocks = append(m.blocks, block{kind: "text", content: "# Heading\n\nSome **bold** text."})

	plain := ansi.Strip(m.renderBlock(0, m.blocks[0]))
	if strings.Contains(plain, "**bold**") {
		t.Fatalf("markdown was not rendered for a text block: %q", plain)
	}
}
