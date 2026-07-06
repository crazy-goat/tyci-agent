package display

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/tools"
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
		elapsed := time.Since(m.requestStartTime)
		if elapsed < 0 {
			elapsed = 0
		}
		elapsedSuffix := fmt.Sprintf(" %.1fs", elapsed.Seconds())

		switch m.status {
		case "thinking":
			leftParts = append(leftParts, "⟳ thinking..."+elapsedSuffix)
		case "responding":
			leftParts = append(leftParts, "⟳ responding..."+elapsedSuffix)
		case "tool":
			leftParts = append(leftParts, "⟳ tool..."+elapsedSuffix)
		default:
			leftParts = append(leftParts, "⟳ working..."+elapsedSuffix)
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

	// Right-align the right part, with leading and trailing space.
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	padding := m.width - leftW - rightW
	if padding >= 2 {
		return " " + left + strings.Repeat(" ", padding-2) + right + " "
	}
	// Not enough room for both spaces; just show leading space.
	if padding >= 1 {
		return " " + left + strings.Repeat(" ", padding-1) + right
	}
	return left + right
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
// working directory and tool/skill/MCP context counts. The bar is exactly
// m.width wide with a dark background. The path is left-aligned; the
// counters are right-aligned. Long paths are truncated with a leading "…",
// keeping the tail visible. Counters have rendering priority: the path is
// truncated first. If even with a truncated path the total exceeds m.width,
// counters are dropped in order: mcp first, then tools, then skills (the
// path is never dropped). A single leading and trailing space is included.
func (m TuiModel) buildTopBar() string {
	path := displayPath(m.cwd, m.home)

	// ── Counter definitions ─────────────────────────────────────────────
	type counterDef struct {
		label     string
		value     string
		dropOrder int // 1 = dropped first
	}

	// Fetch current todo counts from the tools package. These can change
	// during a session as the model adds/completes items via the todo tool.
	todoDone, todoTotal := tools.TodoCounts()
	todoStr := fmt.Sprintf("%d/%d", todoDone, todoTotal)
	if todoTotal == 0 {
		todoStr = "-"
	}

	counters := []counterDef{
		{label: "todos:", value: todoStr, dropOrder: 3},
		{label: "skills:", value: fmt.Sprintf("%d", m.skillCount), dropOrder: 4},
		{label: "tools:", value: fmt.Sprintf("%d", m.toolCount), dropOrder: 2},
		{label: "mcp:", value: fmt.Sprintf("%d", m.mcpCount), dropOrder: 1},
	}

	renderCounter := func(c counterDef) string {
		return fmt.Sprintf("%s %s", c.label, c.value)
	}

	type activeCounter struct {
		def      counterDef
		rendered string
	}
	active := make([]activeCounter, len(counters))
	for i, c := range counters {
		active[i] = activeCounter{def: c, rendered: renderCounter(c)}
	}

	sep := " "
	sepW := lipgloss.Width(sep)

	// Leading and trailing padding: 1 space on each side.
	const sidePad = 1
	sidePadW := sidePad * 2

	// ── Iteratively drop counters until everything fits ─────────────────
	for {
		counterStrs := make([]string, len(active))
		for i, a := range active {
			counterStrs[i] = a.rendered
		}
		counterGroup := strings.Join(counterStrs, sep)
		counterW := lipgloss.Width(counterGroup)

		availableForPath := m.width - sidePadW - counterW - sepW
		if availableForPath < 1 {
			availableForPath = 1
		}

		truncatedPath := path
		if lipgloss.Width(truncatedPath) > availableForPath {
			runes := []rune(truncatedPath)
			for len(runes) > 1 {
				candidate := "…" + string(runes[1:])
				if lipgloss.Width(candidate) <= availableForPath {
					truncatedPath = candidate
					break
				}
				runes = runes[1:]
			}
			if lipgloss.Width(truncatedPath) > availableForPath {
				truncatedPath = "…"
			}
		}

		pathW := lipgloss.Width(truncatedPath)
		total := sidePadW + pathW + sepW + counterW
		if total <= m.width {
			padding := m.width - pathW - sepW - counterW - sidePadW
			if padding < 0 {
				padding = 0
			}
			content := strings.Repeat(" ", sidePad) + truncatedPath + strings.Repeat(" ", padding) + sep + counterGroup
			// Let lipgloss pad the remaining width (provides trailing space).
			style := lipgloss.NewStyle().
				Width(m.width).MaxWidth(m.width).
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("250"))
			return style.Render(content)
		}

		// Doesn't fit — drop the counter with the lowest dropOrder.
		if len(active) == 0 {
			break
		}
		minIdx := 0
		for i := 1; i < len(active); i++ {
			if active[i].def.dropOrder < active[minIdx].def.dropOrder {
				minIdx = i
			}
		}
		active = append(active[:minIdx], active[minIdx+1:]...)
	}

	// ── No counters fit — show path only, truncated to width ────────────
	avail := m.width - sidePadW
	if avail < 1 {
		avail = 1
	}
	if lipgloss.Width(path) > avail {
		runes := []rune(path)
		for len(runes) > 1 {
			candidate := "…" + string(runes[1:])
			if lipgloss.Width(candidate) <= avail {
				path = candidate
				break
			}
			runes = runes[1:]
		}
		if lipgloss.Width(path) > avail {
			path = "…"
		}
	}
	content := strings.Repeat(" ", sidePad) + path
	style := lipgloss.NewStyle().
		Width(m.width).MaxWidth(m.width).
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("250"))
	return style.Render(content)
}

// ─── Public API ───────────────────────────────────────────────────────────
