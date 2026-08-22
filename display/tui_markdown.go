package display

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
)

var (
	muRendererCache sync.Mutex
	rendererCache   = make(map[int]*glamour.TermRenderer)
)

// inlineCodeFg is the colour of `inline code` spans.
//
// glamour's built-in dark style uses 203, a bright red. In a terminal that is
// the colour of something being wrong: errors, failed tests, deleted lines.
// Inline code is the most common span in an agent's answers — every file name
// and function name is one — so a whole reply reads as a page of warnings.
// 180 is a warm sand that stays legible on the 236 background glamour puts
// behind code, and does not collide with the blue of headings, the grey of
// thinking, or the red that genuinely does mean failure.
const inlineCodeFg = "180"

// markdownStyle is glamour's dark style with inline code recoloured. Built
// fresh on each call rather than kept as a package var: StyleConfig is a
// struct of pointers, and handing out a shared one invites a later caller to
// mutate the copy every renderer shares.
func markdownStyle() ansi.StyleConfig {
	style := styles.DarkStyleConfig
	fg := inlineCodeFg
	style.Code.Color = &fg
	return style
}

// getRenderer returns a cached glamour TermRenderer for the given wrap width,
// creating one if needed.
func getRenderer(maxW int) *glamour.TermRenderer {
	muRendererCache.Lock()
	defer muRendererCache.Unlock()
	if r, ok := rendererCache[maxW]; ok {
		return r
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle()),
		glamour.WithWordWrap(maxW),
	)
	if err != nil {
		// Fallback: return nil; callers should handle
		return nil
	}
	rendererCache[maxW] = r
	return r
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
			// glamour leaves whitespace-only padding lines and does not
			// collapse blank runs like the streaming wrapper does — normalize
			// them so force-render output matches streamWrap's line count.
			out = collapseMarkdownBlankLines(out)
			if useBar {
				bar := thinkingBarStyle.Render("│")
				textStyle := thinkingStyle
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
				// Thinking is never markdown: glamour emits its own colours,
				// and each of its resets would drop the text back to the
				// default foreground mid-line, so the block comes out
				// streaked instead of uniformly grey. This is the third place
				// that renders a thinking block (with renderBlock's streaming
				// and settled branches) and they must agree, or the block
				// changes appearance as it is finalised.
				// Same collapse as renderBlock's settled branch: this is the
				// other place a finished block is rendered, and the two must
				// produce the same text or the block changes as it settles.
				content := collapseRepeatedLines(b.content)
				var rendered string
				if b.kind == "thinking" {
					rendered = wrapRawText(content, true, m.width)
				} else {
					rendered = renderMarkdownWithCache(content, false, m.width)
				}
				if rendered != "" {
					m.mdCacheRendered[idx] = rendered
					m.blocks[idx].cachedLineCount = lineCount(rendered)
					m.blocks[idx].cachedLines = strings.Split(rendered, "\n")
				} else {
					// Empty render → keep the empty-block invariant so the next
					// access doesn't build a bogus [""] cache.
					m.blocks[idx].cachedLines = []string{}
					m.blocks[idx].cachedLineCount = 0
				}
				m.blocks[idx].dirty = false
				delete(m.dirtyBlocks, idx)
				delete(m.streamWraps, idx)
			} else if b.kind == "error" || b.kind == "block" {
				rendered := renderErrorOrBlock(b, m.width)
				if rendered != "" {
					m.mdCacheRendered[idx] = rendered
					m.blocks[idx].cachedLineCount = lineCount(rendered)
					m.blocks[idx].cachedLines = strings.Split(rendered, "\n")
				} else {
					m.blocks[idx].cachedLines = []string{}
					m.blocks[idx].cachedLineCount = 0
				}
				m.blocks[idx].dirty = false
				delete(m.dirtyBlocks, idx)
				delete(m.streamWraps, idx)
			} else {
				delete(m.dirtyBlocks, idx)
				delete(m.streamWraps, idx)
			}
		}
	}
}
