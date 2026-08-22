package display

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// streamWrap incrementally wraps a streaming block's raw text. Content only
// grows during streaming, and appended text can never change the wrapping of
// logical lines that already ended with a newline — so those keep their
// wrapped form and only the last (incomplete) logical line is re-wrapped on
// each render. Must be discarded when the wrap width changes or the block
// finishes.
type streamWrap struct {
	srcLen      int      // bytes of content whose wrapping is final (ends right after a '\n')
	stableLines []string // wrapped sub-lines for content[:srcLen]
	lastLen     int      // content length at the previous render
	lastOut     string   // full wrapped output for lastLen
	lastLines   []string // split lines matching lastOut
}

// render returns the wrapped output for content, reusing wrapped lines for
// all completed logical lines. The result is identical to
// wrapRawText(content, useBar, width).
func (sw *streamWrap) render(content string, useBar bool, width int) (string, []string) {
	if sw.srcLen > len(content) || sw.lastLen > len(content) {
		// Content shrank — invariant broken, restart from scratch.
		*sw = streamWrap{}
	}
	if sw.lastLen == len(content) && sw.lastLines != nil {
		return sw.lastOut, sw.lastLines
	}
	w := newRawWrapper(useBar, width)
	if nl := strings.LastIndexByte(content, '\n'); nl+1 > sw.srcLen {
		parts := strings.Split(content[sw.srcLen:nl+1], "\n")
		// The trailing element after the final '\n' is the start of the next
		// (incomplete) logical line — always empty here, skip it.
		for _, line := range parts[:len(parts)-1] {
			sw.stableLines = w.appendLogicalLine(sw.stableLines, line)
		}
		sw.srcLen = nl + 1
	}
	lines := make([]string, len(sw.stableLines), len(sw.stableLines)+4)
	copy(lines, sw.stableLines)
	lines = w.appendLogicalLine(lines, content[sw.srcLen:])
	// Drop trailing empty sub-lines to match wrapRawText's TrimRight.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	out := strings.Join(lines, "\n")
	if out == "" {
		lines = []string{""}
	}
	sw.lastLen = len(content)
	sw.lastOut = out
	sw.lastLines = lines
	return out, lines
}

func (m TuiModel) renderBlock(idx int, b block) string {
	// ── Helper to render markdown with caching ──
	// dim=true renders the block as grey raw text instead of markdown.
	// Deliberately not "markdown, then tint": glamour emits its own colour
	// codes and each of its resets would drop the text back to the default
	// foreground mid-line, so the block would come out streaked rather than
	// uniformly grey. Reasoning text gains little from markdown anyway.
	tryRenderMarkdown := func(content string, useBar, dim bool) string {
		if content == "" {
			return ""
		}
		cached, hasCached := m.mdCacheRendered[idx]
		dirty := m.dirtyBlocks[idx]
		isStreaming := m.status != "idle"

		// If not dirty and cache exists → return cached
		if !dirty && hasCached && cached != "" {
			return cached
		}

		// During streaming: show raw wrapped text (no glamour re-render).
		// Markdown rendering only happens when block finishes (idle or
		// forceRenderDirtyBlocks called at block boundary). Wrapping is
		// incremental: only the last logical line is re-wrapped per chunk.
		if dirty && isStreaming {
			sw := m.streamWraps[idx]
			if sw == nil {
				sw = &streamWrap{}
				m.streamWraps[idx] = sw
			}
			wrapped, lines := sw.render(content, useBar, m.width)
			m.blocks[idx].cachedLineCount = len(lines)
			m.blocks[idx].cachedLines = lines
			return wrapped
		}

		// Not streaming → final rendering. The block has settled, so a run
		// of identical lines can be collapsed now; doing it while streaming
		// would rewrite the text the incremental wrapper has already wrapped.
		content = collapseRepeatedLines(content)
		var rendered string
		if dim {
			rendered = wrapRawText(content, useBar, m.width)
		} else {
			rendered = renderMarkdownWithCache(content, useBar, m.width)
		}
		// Update cache
		delete(m.dirtyBlocks, idx)
		delete(m.streamWraps, idx)
		if rendered == "" {
			// Empty render → keep the empty-block invariant: cachedLines is
			// non-nil but empty, cachedLineCount == 0. Never cache [""] which
			// would show up as a spurious blank line.
			m.blocks[idx].cachedLines = []string{}
			m.blocks[idx].cachedLineCount = 0
			return rendered
		}
		m.mdCacheRendered[idx] = rendered
		m.blocks[idx].cachedLineCount = lineCount(rendered)
		m.blocks[idx].cachedLines = strings.Split(rendered, "\n")
		return rendered
	}

	switch b.kind {
	case "thinking":
		return m.renderThinkingBlock(idx, b)
	case "text":
		return tryRenderMarkdown(b.content, false, false)
	case "user":
		return wrapRawText(b.content, false, m.width)
	case "tool":
		return m.renderToolBlock(idx, b)
	case "usage":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("247")).Render("📊 " + b.content)
	case "error":
		if cached, ok := m.mdCacheRendered[idx]; ok && cached != "" && !m.dirtyBlocks[idx] {
			return cached
		}
		return renderErrorOrBlock(b, m.width)
	case "block":
		if cached, ok := m.mdCacheRendered[idx]; ok && cached != "" && !m.dirtyBlocks[idx] {
			return cached
		}
		return renderErrorOrBlock(b, m.width)
	default:
		return b.content
	}
}

// getBlockLines returns the cached lines for a block, computing them if necessary.
// If forceRender is false and the block has no cached lines, it calls renderBlock to compute them.
// Returns nil if the block is empty or has no content.
//
// If the block was flushed to the scrollback cache (old history paged out to
// disk), this pages its rendered lines back in first. Callers on the render /
// scroll path go through here, so scroll-up transparently restores old blocks.
func (m *TuiModel) getBlockLines(idx int, forceRender bool) []string {
	if idx < 0 || idx >= len(m.blocks) {
		return nil
	}
	b := &m.blocks[idx]
	if b.cachedLines != nil {
		return b.cachedLines
	}
	// Flushed to disk? Page it back in (restores b.cachedLines).
	if b.flushed {
		if lines := m.ensureBlockResident(idx); lines != nil {
			return lines
		}
		// page-in failed → treat as empty
		return nil
	}
	if forceRender {
		return nil
	}
	rendered := m.renderBlock(idx, *b)
	if rendered == "" {
		b.cachedLines = []string{} // empty but not nil → skip next time
		b.cachedLineCount = 0
		return nil
	}
	if b.cachedLines != nil {
		// renderBlock populated the line cache itself — avoid re-splitting.
		return b.cachedLines
	}
	lines := strings.Split(rendered, "\n")
	b.cachedLines = lines
	b.cachedLineCount = len(lines)
	// Track resident bytes so maybeFlushOldBlocks knows when to evict.
	m.scrollback.residentBytes += blockLinesBytes(lines)
	return lines
}

// renderErrorOrBlock renders error and block type blocks (no glamour, just wrapText).
func renderErrorOrBlock(b block, width int) string {
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("│")
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Italic(true)
	if b.kind == "block" {
		bar = lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Render("│")
		textStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Italic(true)
	}
	maxW := width - 2
	if maxW < 10 {
		maxW = 10
	}
	var out strings.Builder
	for _, line := range strings.Split(b.content, "\n") {
		wrapped := wrapText(line, maxW, 0)
		for _, wl := range strings.Split(wrapped, "\n") {
			wl = strings.TrimSuffix(wl, clearLine)
			out.WriteString(bar)
			out.WriteString(" ")
			out.WriteString(textStyle.Render(wl))
			out.WriteString("\n")
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

// thinkingStyle greys out a model's reasoning so it is legible but visibly
// secondary to the answer. Adaptive because the TUI runs on light and dark
// terminals and a single grey is unreadable on one of them.
var thinkingStyle = lipgloss.NewStyle().Foreground(thinkingFg).Italic(true)

// thinkingBarStyle colours the "│ " gutter that marks a thinking block. Same
// grey as the text: a coloured bar next to grey text reads as an accent, and
// the whole block is meant to recede.
var thinkingBarStyle = lipgloss.NewStyle().Foreground(thinkingFg)

// thinkingFg is adaptive because the TUI runs on light and dark terminals and
// a single grey is unreadable on one of them.
var thinkingFg = lipgloss.AdaptiveColor{Light: "245", Dark: "243"}
