package display

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m TuiModel) renderBlock(idx int, b block) string {
	// ── Helper to render markdown with caching ──
	tryRenderMarkdown := func(content string, useBar bool) string {
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
		// forceRenderDirtyBlocks called at block boundary).
		if dirty && isStreaming {
			if sc, ok := m.streamingCache[idx]; ok {
				return sc
			}
			wrapped := wrapRawText(content, useBar, m.width)
			m.streamingCache[idx] = wrapped
			m.blocks[idx].cachedLineCount = lineCount(wrapped)
			m.blocks[idx].cachedLines = strings.Split(wrapped, "\n")
			return wrapped
		}

		// Not streaming → do glamour render (final rendering)
		rendered := renderMarkdownWithCache(content, useBar, m.width)
		// Update cache
		delete(m.dirtyBlocks, idx)
		delete(m.streamingCache, idx)
		m.mdCacheRendered[idx] = rendered
		m.blocks[idx].cachedLineCount = lineCount(rendered)
		m.blocks[idx].cachedLines = strings.Split(rendered, "\n")
		delete(m.streamingCache, idx)
		return rendered
	}

	switch b.kind {
	case "thinking":
		return tryRenderMarkdown(b.content, true)
	case "text":
		return tryRenderMarkdown(b.content, false)
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
func (m *TuiModel) getBlockLines(idx int, forceRender bool) []string {
	if idx < 0 || idx >= len(m.blocks) {
		return nil
	}
	b := &m.blocks[idx]
	if b.cachedLines != nil {
		return b.cachedLines
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
	lines := strings.Split(rendered, "\n")
	b.cachedLines = lines
	b.cachedLineCount = len(lines)
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
