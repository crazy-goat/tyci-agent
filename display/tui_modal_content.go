package display

import (
	"encoding/json"
	"strings"
)

// subagentModalText returns the text the modal currently shows: the .output
// buffer of the block it looks at. There is no separate modal copy, so the
// text survives modal close/reopen, other tools starting, and other blocks
// being opened in the meantime.
//
// blockIdx < 0 means the modal isn't looking at a tool block at all —
// subagentModalStaticText instead, used by the jobs modal (Ctrl+B) to show
// a background job's Result/Err. A job is not a tool block (its "subagent"
// tool call already returned by the time it's visible there — see
// runAsync's doc comment in tools/subagent.go), so it has nothing in
// m.blocks to point at.
func (m TuiModel) subagentModalText() string {
	idx := m.subagentModalBlockIdx
	if idx < 0 {
		return m.subagentModalStaticText
	}
	if idx >= len(m.blocks) {
		return ""
	}
	b := m.blocks[idx]
	if b.output != "" {
		return b.output
	}
	// A subagent block's .content is the raw args JSON (rendered as the inline
	// summary), never modal text; other tools fall back to it when they
	// produced no separate output.
	if b.toolName == "subagent" {
		return ""
	}
	return b.content
}

// subagentModalWrappedLines returns the modal's current text wrapped to the
// popup's content width. Every place that measures or renders the modal
// body goes through this one function — a previous scroll-accounting bug
// (tui_scroll.go, fixed on main) came from exactly this kind of line math
// living in two places that quietly stopped agreeing. Before this, a long
// logical line (a thinking block's reasoning is prose: one paragraph is a
// single logical line of hundreds of characters) was hard-truncated with no
// way to scroll to the rest — this wraps it instead, so nothing the block
// wrote is unreachable.
func (m TuiModel) subagentModalWrappedLines() []string {
	maxW := m.subagentModalLayout().popupWidth - 4
	if maxW < 1 {
		maxW = 1
	}
	content := m.subagentModalText()
	lines := make([]string, 0, strings.Count(content, "\n")+1)
	for _, logical := range strings.Split(content, "\n") {
		wrapped := wrapText(logical, maxW, 0)
		for _, wl := range strings.Split(wrapped, "\n") {
			lines = append(lines, strings.TrimSuffix(wl, clearLine))
		}
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

// subagentModalLineCount counts the display lines of the current modal
// text, after wrapping — the count scroll math and the render path both
// need.
func (m TuiModel) subagentModalLineCount() int {
	return len(m.subagentModalWrappedLines())
}

// clampSubagentModalScroll keeps the scroll offset inside the current text.
// The per-block output cap (capToolOutput) drops lines from the top, which
// shrinks maxScroll; without this the modal would render an empty range.
func (m *TuiModel) clampSubagentModalScroll() {
	if m.subagentModalScroll < 0 {
		m.subagentModalScroll = 0
		return
	}
	if maxScroll := m.subagentModalMaxScroll(); m.subagentModalScroll > maxScroll {
		m.subagentModalScroll = maxScroll
	}
}

// openToolBlockModal points the modal at a tool (or thinking) block. It only
// changes what the modal looks at — the block's collected output/content is
// never touched.
func (m *TuiModel) openToolBlockModal(idx int) {
	if idx < 0 || idx >= len(m.blocks) {
		return
	}
	if kind := m.blocks[idx].kind; kind != "tool" && kind != "thinking" {
		return
	}
	if m.blocks[idx].flushed {
		m.ensureBlockResident(idx)
	}
	m.subagentModalActive = true
	m.subagentModalBlockIdx = idx
	m.subagentModalScroll = 0
	m.subagentModalDone = m.blocks[idx].toolState == "done"
	m.subagentModalTitle = m.modalTitleForBlock(idx)
}

// modalTitleForBlock derives the modal heading from the block itself, so it is
// always the title of what is on screen.
func (m TuiModel) modalTitleForBlock(idx int) string {
	b := m.blocks[idx]
	if b.kind == "thinking" {
		return "thinking"
	}
	if b.toolName == "subagent" {
		var args map[string]any
		if json.Unmarshal([]byte(b.content), &args) == nil {
			if title := subagentTitleFromArgs(args); title != "" {
				return truncateString(title, 80)
			}
		}
		return "subagent"
	}
	if b.content != "" {
		if firstLine := strings.SplitN(b.content, "\n", 2)[0]; firstLine != "" {
			return truncateString(firstLine, 80)
		}
	}
	return b.toolName
}
