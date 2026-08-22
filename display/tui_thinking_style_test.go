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

// TestThinkingIsGreyOnEveryPath is the test that would have caught the first
// attempt. A thinking block is rendered by THREE code paths — streaming,
// settled, and forceRenderDirtyBlocks at a block boundary — and the last one
// went through glamour, so it painted the block back to its old colour after
// the other two had been changed. They must all produce the same bytes.
func TestThinkingIsGreyOnEveryPath(t *testing.T) {
	const content = "reasoning about the problem"

	settled := thinkingModel()
	settled.blocks = append(settled.blocks, block{kind: "thinking", content: content})
	want := settled.renderBlock(0, settled.blocks[0])

	streaming := thinkingModel()
	streaming.status = "streaming"
	streaming.blocks = append(streaming.blocks, block{kind: "thinking", content: content})
	streaming.dirtyBlocks[0] = true
	if got := streaming.renderBlock(0, streaming.blocks[0]); got != want {
		t.Errorf("streaming path differs:\n got %q\nwant %q", got, want)
	}

	forced := thinkingModel()
	forced.blocks = append(forced.blocks, block{kind: "thinking", content: content})
	forced.dirtyBlocks[0] = true
	forced.forceRenderDirtyBlocks()
	if got := forced.mdCacheRendered[0]; got != want {
		t.Errorf("force-render path differs:\n got %q\nwant %q", got, want)
	}
}

// TestThinkingGoesThroughTheDimPathNotMarkdown is the behavioural half: the
// block must be raw wrapped text run through dimLines, and NOT glamour, whose
// own colour resets would streak the grey mid-line.
func TestThinkingBlockUsesTheDimPipeline(t *testing.T) {
	const content = "weighing two options here"

	m := thinkingModel()
	m.blocks = append(m.blocks, block{kind: "thinking", content: content})
	rendered := m.renderBlock(0, m.blocks[0])

	want := wrapRawText(content, true, m.width)
	if rendered != want {
		t.Fatalf("thinking block did not take the dim raw-text path:\n got %q\nwant %q", rendered, want)
	}
	if !strings.Contains(ansi.Strip(rendered), content) {
		t.Fatalf("the text itself must survive: %q", ansi.Strip(rendered))
	}
}

// TestThinkingKeepsItsGutter: selection measures the "│ " prefix per source
// kind (see gutterLen), so losing it would misalign copied text.
func TestThinkingKeepsItsGutter(t *testing.T) {
	m := thinkingModel()
	m.blocks = append(m.blocks, block{kind: "thinking", content: "line one\nline two"})

	plain := ansi.Strip(m.renderBlock(0, m.blocks[0]))
	for i, line := range strings.Split(plain, "\n") {
		if line == "" {
			continue
		}
		if gutterLen("thinking", line) != 2 {
			t.Fatalf("row %d lost its gutter: %q", i, line)
		}
	}
}

// TestThinkingLineCountIsPreserved is the invariant the transcript depends on:
// the styling step must not add or drop rows, or the viewport's line
// accounting drifts from what is on screen.
func TestThinkingLineCountIsPreserved(t *testing.T) {
	content := "first\nsecond\nthird"
	wantRows := len(strings.Split(wrapRawText(content, true, 60), "\n"))

	m := thinkingModel()
	m.blocks = append(m.blocks, block{kind: "thinking", content: content})

	got := len(strings.Split(m.renderBlock(0, m.blocks[0]), "\n"))
	if got != wantRows {
		t.Fatalf("styling changed the row count: %d vs %d", got, wantRows)
	}
	if lines := m.getBlockLines(0, false); len(lines) != wantRows {
		t.Fatalf("cachedLines has %d rows, rendered %d", len(lines), wantRows)
	}
}

// TestThinkingLooksTheSameWhileStreaming: the incremental wrap path is a
// separate branch, and a block that only turns grey once it finishes would
// visibly flicker as the answer arrives.
func TestThinkingLooksTheSameWhileStreaming(t *testing.T) {
	const content = "partial reasoning"

	streaming := thinkingModel()
	streaming.status = "streaming"
	streaming.blocks = append(streaming.blocks, block{kind: "thinking", content: content})
	streaming.dirtyBlocks[0] = true

	settled := thinkingModel()
	settled.blocks = append(settled.blocks, block{kind: "thinking", content: content})

	got := streaming.renderBlock(0, streaming.blocks[0])
	want := settled.renderBlock(0, settled.blocks[0])
	if got != want {
		t.Fatalf("streaming and settled renders differ, so the block would change on completion:\n got %q\nwant %q", got, want)
	}
}

// TestTextBlockIsNotDimmed: the answer must keep its own markdown styling.
func TestTextBlockIsNotDimmed(t *testing.T) {
	m := thinkingModel()
	m.blocks = append(m.blocks, block{kind: "text", content: "# Heading\n\nSome **bold** text."})

	plain := ansi.Strip(m.renderBlock(0, m.blocks[0]))
	if strings.Contains(plain, "**bold**") {
		t.Fatalf("markdown was not rendered for a text block: %q", plain)
	}
}
