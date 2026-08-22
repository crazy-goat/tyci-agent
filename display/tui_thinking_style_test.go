package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
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
// "│ thinking" prefix per source kind (see gutterLen), so losing it would
// misalign copied text — the same concern "tool"'s "┃ tool " prefix has.
// No space before the opening paren: the label already says "thinking", so
// the line runs straight into "(summary) ..." rather than repeating the
// word.
func TestThinkingKeepsItsGutter(t *testing.T) {
	m := thinkingModel()
	m.blocks = append(m.blocks, block{kind: "thinking", content: "line one\nline two", thinkingSummary: "line one line two"})

	plain := ansi.Strip(m.renderBlock(0, m.blocks[0]))
	if !strings.HasPrefix(plain, "│ thinking(") {
		t.Fatalf("collapsed line should start with the bar+label gutter, got: %q", plain)
	}
	got := gutterLen("thinking", plain)
	if got != len([]rune("│ thinking")) {
		t.Fatalf("gutterLen(\"thinking\", ...) = %d, want %d", got, len([]rune("│ thinking")))
	}
	if stripped := string([]rune(plain)[got:]); !strings.HasPrefix(stripped, "(line one line two)") {
		t.Fatalf("stripping the gutter should leave the summary, got: %q", stripped)
	}
}

// TestThinkingBlockActuallyUsesTheThinkingStyles: TestThinkingStyleIsGrey
// only pins the two style *variables'* configuration; nothing asserted that
// renderThinkingBlock actually applies them. Under a bare `go test` lipgloss
// detects no terminal and every Style.Render call degrades to the plain
// input string (termenv's Ascii profile — see its Style.Styled: "if
// t.profile == Ascii { return s }"), which is exactly why that comment says
// asserting on emitted ANSI is normally unreliable here. Forcing the
// profile with lipgloss.SetColorProfile (documented as existing "mostly for
// testing purposes") makes the styling real for this one assertion.
func TestThinkingBlockActuallyUsesTheThinkingStyles(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(orig)

	m := thinkingModel()
	m.blocks = append(m.blocks, block{kind: "thinking", content: "reasoning about the fix", thinkingSummary: "reasoning about the fix"})

	rendered := m.renderBlock(0, m.blocks[0])

	if ansi.Strip(rendered) == rendered {
		t.Fatalf("forcing a color profile produced no ANSI codes at all — the test setup is not exercising styling: %q", rendered)
	}

	// The gutter bar and label are exactly thinkingBarStyle's and an inline
	// bold-thinkingFg style's own rendering, concatenated — reconstruct that
	// literally and require it as a prefix.
	wantBar := thinkingBarStyle.Render("│")
	wantLabel := lipgloss.NewStyle().Foreground(thinkingFg).Bold(true).Render("thinking")
	wantPrefix := wantBar + " " + wantLabel
	if !strings.HasPrefix(rendered, wantPrefix) {
		t.Fatalf("collapsed line does not start with the bar+label exactly as thinkingBarStyle/thinkingFg would render them:\n got  %q\nwant prefix %q", rendered, wantPrefix)
	}

	// Everything after that prefix is rendered as one thinkingStyle.Render
	// call, so it must open with thinkingStyle's own escape and close with
	// its own reset — derive both from thinkingStyle itself rather than
	// hardcoding an SGR sequence.
	const marker = "\x00"
	wrapped := thinkingStyle.Render(marker)
	openCode, resetCode, ok := strings.Cut(wrapped, marker)
	if !ok || openCode == "" || resetCode == "" {
		t.Fatalf("could not derive thinkingStyle's open/reset codes from a forced profile: %q", wrapped)
	}
	remainder := rendered[len(wantPrefix):]
	if !strings.HasPrefix(remainder, openCode) {
		t.Fatalf("summary span does not open with thinkingStyle's own escape:\n got  %q\nwant prefix %q", remainder, openCode)
	}
	if !strings.HasSuffix(remainder, resetCode) {
		t.Fatalf("summary span does not close with thinkingStyle's own reset:\n got  %q\nwant suffix %q", remainder, resetCode)
	}
	if !strings.Contains(ansi.Strip(remainder), "(reasoning about the fix)") {
		t.Fatalf("summary text missing from the styled span: %q", ansi.Strip(remainder))
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
