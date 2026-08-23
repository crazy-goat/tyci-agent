package display

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// thinkingSummaryMaxLen is the width (in runes) a thinking block's collapsed
// summary is frozen at. Matches the width formatToolCall truncates a tool's
// arg summary to (see e.g. its "find" case), so a thinking line and a tool
// line take up a similar share of the row before any further, width-aware
// truncation happens at render time (see renderThinkingBlock).
const thinkingSummaryMaxLen = 60

// thinkingSummaryPlaceholder stands in for the summary while a thinking
// block is still streaming and hasn't accumulated enough text to decide one
// yet. Showing the ever-growing partial text instead would make the
// collapsed line change on every delta — this fixed placeholder is what
// keeps it stable until freezeThinkingSummary commits to a real one.
const thinkingSummaryPlaceholder = "…"

// freezeThinkingSummary decides, once, the one-line summary shown in a
// thinking block's collapsed render — drawn from the block's opening words,
// newlines collapsed to spaces like truncateResumePrompt does for resume
// entries. Once b.thinkingSummary is non-empty it is never touched again, so
// the collapsed line cannot change (and flicker) as more content streams in.
//
// Called after every delta so the summary is captured as soon as there is
// enough text to fill thinkingSummaryMaxLen — deciding earlier risks a
// summary that later has to grow a "..." it didn't have yet, which would
// itself be a visible change. The threshold is counted in runes, not bytes:
// a byte count freezes multi-byte (e.g. CJK) reasoning far too early and,
// since the freeze is permanent, keeps a much shorter summary than intended
// for the rest of the block's life. force decides early using whatever text
// exists so far; used when the block finalizes with less text than that.
func freezeThinkingSummary(b *block, force bool) {
	if b.thinkingSummary != "" {
		return
	}
	collapsed := strings.Join(strings.Fields(b.content), " ")
	if !force && utf8.RuneCountInString(collapsed) < thinkingSummaryMaxLen {
		return
	}
	if collapsed == "" {
		b.thinkingSummary = thinkingSummaryPlaceholder
		return
	}
	b.thinkingSummary = truncateString(collapsed, thinkingSummaryMaxLen)
}

// renderThinkingBlock renders a thinking block as a single collapsed line,
// mirroring renderToolBlock: a bar, a bold label, a one-line summary, a
// duration once the block has finished, and the exact same "click to
// display" / "click for progress" affordance — thinking is meant to read as
// a sibling of a tool call, not as a second, differently-shaped widget.
func (m TuiModel) renderThinkingBlock(b block) string {
	bar := thinkingBarStyle.Render("│")
	label := lipgloss.NewStyle().Foreground(thinkingFg).Bold(true).Render("thinking")
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	var status, hint string
	if b.toolState == "done" {
		status = " " + formatDuration(b.duration)
		hint = "- click to display"
	} else {
		status = " ⟳"
		hint = "- click for progress"
	}

	summary := b.thinkingSummary
	if summary == "" {
		summary = thinkingSummaryPlaceholder
	}
	// Budget the summary against the width this row actually has. The
	// freeze above already caps it at thinkingSummaryMaxLen runes, but that
	// is width-blind — a run of CJK characters at that rune count is nearly
	// twice as many display columns — and even a plain-ASCII summary can
	// still overflow a narrow terminal once the fixed parts of the line
	// (gutter, label, parens, duration, hint) are accounted for.
	fixed := lipgloss.Width("│ thinking()" + status + " " + hint)
	// Zero, not one: at a width so narrow the fixed decoration alone fills
	// the row, the summary should shrink to nothing rather than force one
	// more column of overflow on top of it (truncateToWidth already
	// returns "" for width <= 0).
	//
	// renderWidth, not m.width: this renders through getBlockLines even on
	// the real model while the sidebar is open (totalRenderedLines touches
	// every block), and the only thing on screen is the narrowed main
	// column — budgeting against the full terminal width would let the
	// collapsed line overflow into the sidebar.
	avail := m.renderWidth() - fixed
	if avail < 0 {
		avail = 0
	}
	summary = truncateToWidth(summary, avail)

	// No space between the label and "(" — the label already says
	// "thinking"; repeating the word in the line itself ("thinking
	// thinking(...)") would just be noise.
	line := "(" + summary + ")" + status + " " + hintStyle.Render(hint)
	return bar + " " + label + thinkingStyle.Render(line)
}
