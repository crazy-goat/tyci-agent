package display

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// A run of two or more consecutive tool and thinking blocks is a group. In the
// transcript a group is one header line. Its blocks are drawn below the header
// only while the group is open. Groups are closed by default. A single tool or
// thinking block is not grouped and draws as before.
//
// The transcript is laid out in units: one block, or one group from its first
// block to its last. The line counts (totalRenderedLines, visibleLine) and the
// flat-line builders all walk the units through the helpers below, so they
// agree on what is drawn.

// unitEnd returns the index of the last block of the layout unit that starts
// at block first. Consecutive tool and thinking blocks form one unit; any other
// block is a unit of its own.
func (m *TuiModel) unitEnd(first int) int {
	last := first
	if !compactKind(m.blocks[first].kind) {
		return last
	}
	for last+1 < len(m.blocks) && compactKind(m.blocks[last+1].kind) {
		last++
	}
	return last
}

// blockLineCount returns the number of lines block i draws.
func (m *TuiModel) blockLineCount(i int) int {
	b := &m.blocks[i]
	// A flushed block's cachedLineCount was computed for the width it was
	// flushed at (b.flushedWidth), not necessarily the width the transcript
	// renders at now (renderWidth: full m.width, or the narrowed main column
	// while the sidebar is open). On resize — or a sidebar open/close, which
	// changes renderWidth too — invalidateAllBlockLineCounts deliberately
	// leaves flushed blocks' counts untouched (they're re-wrapped lazily, only
	// when actually scrolled into view) — but that means a nonzero
	// cachedLineCount here can be stale. getBlockLines pages the block back in
	// via ensureBlockResident, which re-wraps for renderWidth and fixes up
	// cachedLineCount, so route through it instead of trusting the cached
	// count directly. Without this, the transcript total silently disagrees
	// with what the flat-line builders actually produce (they always call
	// getBlockLines), which shows up as bogus viewport-pad rows hiding real
	// scrollback content.
	stale := b.flushed && b.flushedWidth != 0 && b.flushedWidth != m.renderWidth()
	if b.cachedLineCount != 0 && !stale {
		return b.cachedLineCount
	}
	return len(m.getBlockLines(i, false))
}

// unitLineCount returns the number of lines the unit first..last draws, not
// counting its spacer.
func (m *TuiModel) unitLineCount(first, last int) int {
	if first == last {
		return m.blockLineCount(first)
	}
	// The blocks of a closed group are measured although they are not drawn.
	// That keeps their lines cached, which the scrollback flush needs before it
	// can write them out (see maybeFlushOldBlocks).
	lines := 0
	for i := first; i <= last; i++ {
		lines += m.blockLineCount(i)
	}
	if !m.blocks[first].groupOpen {
		return 1
	}
	return 1 + lines
}

// unitFlatLines returns the lines the unit first..last draws, in order, not
// counting its spacer. A closed group draws only its header line.
func (m *TuiModel) unitFlatLines(first, last int) []flatRenderLine {
	var out []flatRenderLine
	if first < last {
		out = append(out, flatRenderLine{
			Text:       m.groupHeaderLine(first, last),
			SourceKind: "group",
			BlockIndex: first,
			SourceLine: 0,
		})
		if !m.blocks[first].groupOpen {
			return out
		}
	}
	for i := first; i <= last; i++ {
		for j, line := range m.getBlockLines(i, false) {
			out = append(out, flatRenderLine{
				Text:       line,
				SourceKind: m.blocks[i].kind,
				BlockIndex: i,
				SourceLine: j,
			})
		}
	}
	return out
}

// groupHeaderLine renders the header of the group first..last. While a step
// runs it names the step count and the latest step. Once every step has
// finished it names the counts and the total time. The right side tells how
// to toggle the group.
func (m *TuiModel) groupHeaderLine(first, last int) string {
	steps := m.blocks[first : last+1]
	tools, thinking := 0, 0
	active := false
	for _, b := range steps {
		if b.kind == "tool" {
			tools++
		} else {
			thinking++
		}
		if b.toolState != "done" {
			active = true
		}
	}

	var text string
	if active {
		text = fmt.Sprintf("⟳ %d steps · %s", len(steps), stepLabel(m.blocks[last]))
	} else {
		text = fmt.Sprintf("✓ %d steps (%d tool, %d thinking) · %s", len(steps), tools, thinking, formatDuration(m.groupSpan(first, last)))
	}

	hint := "▸ expand"
	if m.blocks[first].groupOpen {
		hint = "▾ collapse"
	}
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("┃")
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// The text gives way to the width, so the hint always stays visible.
	avail := m.renderWidth() - lipgloss.Width("┃ ") - lipgloss.Width("  "+hint)
	text = truncateToWidth(text, avail)
	return bar + " " + textStyle.Render(text) + "  " + hintStyle.Render(hint)
}

// groupSpan returns the total time of the group first..last: from the start of
// its first step to the latest finish of any step. Steps do not end in block
// order. A batch of parallel calls ends with its slowest call, which can be an
// early block.
func (m *TuiModel) groupSpan(first, last int) time.Duration {
	var end time.Time
	for _, b := range m.blocks[first : last+1] {
		if b.endTime.After(end) {
			end = b.endTime
		}
	}
	return end.Sub(m.blocks[first].startTime)
}

// stepLabel names one step of a group the way the step's own line does. The
// duration follows once the step has finished.
func stepLabel(b block) string {
	var label string
	if b.kind == "thinking" {
		summary := b.thinkingSummary
		if summary == "" {
			summary = thinkingSummaryPlaceholder
		}
		label = "thinking(" + summary + ")"
	} else {
		label = "tool " + formatToolCall(b.toolName, b.content)
	}
	if b.toolState == "done" {
		label += " " + formatDuration(b.duration)
	}
	return label
}

// toggleGroup opens a closed group or closes an open one. first is the index
// of the group's first block.
func (m *TuiModel) toggleGroup(first int) {
	m.blocks[first].groupOpen = !m.blocks[first].groupOpen
	m.invalidateTotalLines()
}

// latestGroup returns the index of the first block of the last group in the
// transcript, or -1 when there is no group.
func (m *TuiModel) latestGroup() int {
	found := -1
	for first := 0; first < len(m.blocks); {
		last := m.unitEnd(first)
		if last > first {
			found = first
		}
		first = last + 1
	}
	return found
}
