package display

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

var (
	muRendererCache sync.Mutex
	rendererCache   = make(map[int]*glamour.TermRenderer)
)

// getRenderer returns a cached glamour TermRenderer for the given wrap width,
// creating one if needed.
func getRenderer(maxW int) *glamour.TermRenderer {
	muRendererCache.Lock()
	defer muRendererCache.Unlock()
	if r, ok := rendererCache[maxW]; ok {
		return r
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(maxW),
	)
	if err != nil {
		// Fallback: return nil; callers should handle
		return nil
	}
	rendererCache[maxW] = r
	return r
}

// renderMarkdown renders markdown content to ANSI using glamour.
// Falls back to wrapRawText on error.
func (m TuiModel) renderMarkdown(content string, useBar bool) string {
	return renderMarkdownWithCache(content, useBar, m.width)
}

// renderMarkdownWithCache renders markdown using a cached glamour renderer.
func renderMarkdownWithCache(content string, useBar bool, width int) string {
	if content == "" {
		return ""
	}
	maxW := width
	if useBar {
		maxW = width - 2
	}
	if maxW < 10 {
		maxW = 10
	}
	renderer := getRenderer(maxW)
	if renderer != nil {
		out, err := renderer.Render(content)
		if err == nil {
			out = strings.Trim(out, "\n")
			if useBar {
				bar := lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Render("│")
				textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Italic(true)
				var sb strings.Builder
				for _, line := range strings.Split(out, "\n") {
					// Strip ANSI codes first so TrimLeft works properly
					clean := stripAnsi(line)
					clean = strings.TrimLeft(clean, " ")
					if clean == "" {
						sb.WriteString(bar)
						sb.WriteString("\n")
						continue
					}
					sb.WriteString(bar)
					sb.WriteString(" ")
					sb.WriteString(textStyle.Render(clean))
					sb.WriteString("\n")
				}
				return strings.TrimRight(sb.String(), "\n")
			}
			return out
		}
	}
	return wrapRawText(content, useBar, width)
}

// forceRenderDirtyBlocks immediately glamour-renders all dirty "thinking"
// and "text" blocks, caching the result so they are shown with proper
// markdown formatting. Called when a block finishes (new block type starts
// or streaming ends).
func (m *TuiModel) forceRenderDirtyBlocks() {
	m.invalidateTotalLines()
	for idx := range m.dirtyBlocks {
		if !m.dirtyBlocks[idx] {
			delete(m.dirtyBlocks, idx)
			continue
		}
		if idx >= 0 && idx < len(m.blocks) {
			b := m.blocks[idx]
			if b.kind == "thinking" || b.kind == "text" {
				rendered := renderMarkdownWithCache(b.content, b.kind == "thinking", m.width)
				if rendered != "" {
					m.mdCacheRendered[idx] = rendered
					m.blocks[idx].cachedLineCount = lineCount(rendered)
					m.blocks[idx].cachedLines = strings.Split(rendered, "\n")
				}
				delete(m.dirtyBlocks, idx)
				delete(m.streamingCache, idx)
			} else if b.kind == "error" || b.kind == "block" {
				rendered := renderErrorOrBlock(b, m.width)
				if rendered != "" {
					m.mdCacheRendered[idx] = rendered
					m.blocks[idx].cachedLineCount = lineCount(rendered)
					m.blocks[idx].cachedLines = strings.Split(rendered, "\n")
				}
				delete(m.dirtyBlocks, idx)
				delete(m.streamingCache, idx)
			} else {
				delete(m.dirtyBlocks, idx)
				delete(m.streamingCache, idx)
			}
		}
	}
}
