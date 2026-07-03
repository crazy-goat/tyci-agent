package display

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m TuiModel) buildStatus() string {
	leftParts := []string{}
	rightParts := []string{}

	if m.modelName != "" {
		leftParts = append(leftParts, m.modelName)
	}

	if m.scrollLine > 0 {
		leftParts = append(leftParts, fmt.Sprintf("↑%d lines", m.scrollLine))
	}

	if m.statusMessage != "" {
		leftParts = append(leftParts, m.statusMessage)
	}

	if !m.reading {
		switch m.status {
		case "thinking":
			leftParts = append(leftParts, "⟳ thinking...")
		case "responding":
			leftParts = append(leftParts, "⟳ responding...")
		case "tool":
			leftParts = append(leftParts, "⟳ tool...")
		default:
			leftParts = append(leftParts, "⟳ working...")
		}
	}

	if m.lastUsage.Input > 0 || m.lastUsage.Output > 0 {
		inNew := m.lastUsage.Input - m.lastUsage.CacheRead
		if inNew < 0 {
			inNew = 0
		}
		u := fmt.Sprintf("in=%d", inNew)
		if m.lastUsage.CacheRead > 0 {
			u += fmt.Sprintf(" (+%d cache)", m.lastUsage.CacheRead)
		}
		u += fmt.Sprintf(" out=%d", m.lastUsage.Output)
		if m.lastUsage.Reasoning > 0 {
			u += fmt.Sprintf(" r=%d", m.lastUsage.Reasoning)
		}
		if m.lastUsage.CacheWrite > 0 {
			u += fmt.Sprintf(" cache_w=%d", m.lastUsage.CacheWrite)
		}
		u += fmt.Sprintf(" ctx=%d", m.lastUsage.Input+m.lastUsage.Output)
		genDur := m.lastStats.Duration - m.lastStats.FirstToken
		if genDur < 0 {
			genDur = 0
		}
		u += fmt.Sprintf(" t=%.1fs ttft=%.2fs tok/s=%s",
			m.lastStats.Duration.Seconds(),
			m.lastStats.FirstToken.Seconds(),
			fmtRate(m.lastUsage.Output, genDur),
		)
		rightParts = append(rightParts, u)
	}

	if len(leftParts) == 0 && len(rightParts) == 0 {
		return ""
	}

	left := strings.Join(leftParts, " │ ")
	right := strings.Join(rightParts, " │ ")

	// Right-align the right part
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	padding := m.width - leftW - rightW
	if padding < 1 {
		padding = 1
	}
	return " " + left + strings.Repeat(" ", padding-1) + right
}

// displayPath returns a short, human-friendly representation of the working
// directory.  When cwd falls under home, the home prefix is replaced with "~".
// Empty cwd produces "~?", empty home means the full path is shown as-is.
// Paths are cleaned via filepath.Clean before display.
func displayPath(cwd, home string) string {
	if cwd == "" {
		return "~?"
	}
	c := filepath.Clean(cwd)
	if home == "" {
		return c
	}
	h := filepath.Clean(home)
	if c == h {
		return "~"
	}
	// Strict prefix check with path separator boundary.
	prefix := h + string(filepath.Separator)
	if strings.HasPrefix(c, prefix) {
		return "~" + c[len(h):]
	}
	return c
}

// buildTopBar returns the single-line top status bar showing the current
// working directory.  The bar is exactly m.width wide.  Long paths are
// truncated with a leading "…", keeping the tail visible.
func (m TuiModel) buildTopBar() string {
	path := displayPath(m.cwd, m.home)

	prefix := "📁 "
	prefixW := lipgloss.Width(prefix)
	avail := m.width - prefixW
	if avail < 1 {
		avail = 1
	}

	if lipgloss.Width(path) > avail {
		// Truncate from the left, keeping the tail.  Remove runes one at
		// a time from the front until the path (with a leading "…") fits.
		runes := []rune(path)
		for len(runes) > 1 {
			candidate := "…" + string(runes[1:])
			if lipgloss.Width(candidate) <= avail {
				path = candidate
				break
			}
			runes = runes[1:]
		}
		// Edge case: even "…" alone is too wide — fall back to "…".
		if lipgloss.Width(path) > avail {
			path = "…"
		}
	}

	label := prefix + path
	style := lipgloss.NewStyle().
		Width(m.width).MaxWidth(m.width).
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("250"))
	return style.Render(label)
}

// ─── Public API ───────────────────────────────────────────────────────────
