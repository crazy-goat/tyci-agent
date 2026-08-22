package display

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// thinkingSummaryMaxLen is the width a thinking block's collapsed summary is
// truncated to. Matches the width formatToolCall truncates a tool's arg
// summary to (see e.g. its "find" case), so a thinking line and a tool line
// take up a similar share of the row.
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
// itself be a visible change. force decides early using whatever text
// exists so far; used when the block finalizes with less text than that.
func freezeThinkingSummary(b *block, force bool) {
	if b.thinkingSummary != "" {
		return
	}
	collapsed := strings.Join(strings.Fields(b.content), " ")
	if !force && len(collapsed) < thinkingSummaryMaxLen {
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
func (m TuiModel) renderThinkingBlock(idx int, b block) string {
	bar := thinkingBarStyle.Render("│")
	label := lipgloss.NewStyle().Foreground(thinkingFg).Bold(true).Render("thinking")
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	summary := b.thinkingSummary
	if summary == "" {
		summary = thinkingSummaryPlaceholder
	}
	line := "thinking(" + summary + ")"
	if b.toolState == "done" {
		line += " " + formatDuration(b.duration)
		line += " " + hintStyle.Render("- click to display")
	} else {
		line += " ⟳"
		line += " " + hintStyle.Render("- click for progress")
	}
	return bar + " " + label + " " + thinkingStyle.Render(line)
}
