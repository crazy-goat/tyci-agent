package display

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/internal/gitinfo"
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
		// Cap here, not just at the joined-line truncation below: statusMessage
		// is the one unbounded fragment (job echoes, refusal sentences), and if
		// it's left full-length the tail-truncation below eats the spinner that
		// comes after it in leftParts instead of the message that caused the
		// overflow.
		leftParts = append(leftParts, truncateStatusText(m.statusMessage, 60))
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
			// Named, and timed from the tool's own start. "tool... 13.7s" was
			// the one status that answered neither of the questions a person
			// actually has while watching it: which tool, and how long has
			// THAT been going. The 13.7s was the whole turn's elapsed, so a
			// slow tool one second in looked identical to a wedged one.
			leftParts = append(leftParts, m.runningToolsStatus(elapsedSuffix))
		default:
			leftParts = append(leftParts, "⟳ working..."+elapsedSuffix)
		}
	}

	// Two numbers, not twelve. The per-turn token breakdown, timings and
	// throughput moved to the sidebar's Tokens tab (buildUsageDetail): a
	// status bar is glanced at, and the only two things worth a glance while
	// working are how full the context is and what the session has cost so
	// far. Clicking the context figure opens the tab with the rest.
	if right := m.buildContextCost(); right != "" {
		rightParts = append(rightParts, right)
	}

	if len(leftParts) == 0 && len(rightParts) == 0 {
		return ""
	}

	left := strings.Join(leftParts, " │ ")
	right := strings.Join(rightParts, " │ ")

	// Hard-cap left BEFORE computing padding. Every fragment above is
	// attacker-free but not length-free: m.statusMessage in particular can
	// carry a refusal sentence (tui_keys.go's "/new has to wait — it
	// changes the conversation this turn is writing to..."), which can run
	// well past m.width on its own.
	//
	// This status bar is rendered as one fixed-height row (tui_view.go), via
	// lipgloss which WRAPS content wider than the terminal instead of
	// clipping it — so an unbounded left string doesn't get cut off, it
	// grows the row into several, breaking the TUI's fixed-height layout
	// (observed: a single ~106-char message turned a 20-line frame into
	// 20+ lines). Truncating here, in the one function every status message
	// funnels through, fixes every caller at once instead of each caller
	// remembering to truncate its own message.
	rightW := lipgloss.Width(right)
	maxLeftW := m.width - rightW - 3 // leading space + gap + trailing space
	if maxLeftW < 1 {
		maxLeftW = 1
	}
	left = truncateStatusText(left, maxLeftW)

	// Right-align the right part, with leading and trailing space.
	leftW := lipgloss.Width(left)
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

// truncateStatusText hard-caps s to at most maxW columns (lipgloss.Width,
// which counts display width, not bytes or runes), replacing anything past
// that with a trailing "…". Truncation is rune-based (not byte slicing) —
// see the tui-utf8-truncation-bug history this codebase already fixed at
// several other sites — so a multi-byte character on the cut boundary is
// dropped whole rather than split into invalid UTF-8.
//
// Truncates from the tail, the opposite direction from buildTopBar's
// leading-"…" path truncation: a status message's most useful content is
// usually at its start (which job, what was asked/refused), not its end.
func truncateStatusText(s string, maxW int) string {
	if maxW < 1 {
		maxW = 1
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	runes := []rune(s)
	if len(runes) > maxW {
		runes = runes[:maxW]
	}
	for len(runes) > 0 {
		candidate := string(runes) + "…"
		if lipgloss.Width(candidate) <= maxW {
			return candidate
		}
		runes = runes[:len(runes)-1]
	}
	return "…"
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

// topBarPath is the left-hand side of the top bar: the working directory,
// plus the git branch in parentheses when the directory is in a repository.
// The branch is the tail of the string, so the leading-"…" truncation used by
// the bar drops path segments before it drops the branch — which is the right
// order: you usually know where you are, and less often which branch you left
// yourself on.
func (m TuiModel) topBarPath() string {
	path := displayPath(m.cwd, m.home)
	if branch := gitinfo.Branch(m.cwd); branch != "" {
		path += " (" + branch + ")"
	}
	return path
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
	path := m.topBarPath()

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

// runningToolsStatus describes the tools currently in flight: their names, and
// how long the oldest of them has been running.
//
// fallback is used when the queue is empty — which happens for a moment
// between the status flipping to "tool" and the first tool-start block
// arriving, and would otherwise show a bare "⟳".
func (m TuiModel) runningToolsStatus(fallback string) string {
	var names []string
	oldest := time.Time{}
	for _, idx := range m.toolQueue {
		if idx < 0 || idx >= len(m.blocks) {
			continue
		}
		b := m.blocks[idx]
		if b.kind != "tool" || b.toolState != "running" {
			continue
		}
		names = append(names, b.toolName)
		if oldest.IsZero() || b.startTime.Before(oldest) {
			oldest = b.startTime
		}
	}
	if len(names) == 0 {
		return "⟳ tool..." + fallback
	}

	age := ""
	if !oldest.IsZero() {
		if d := time.Since(oldest); d >= 0 {
			age = fmt.Sprintf(" %.1fs", d.Seconds())
		}
	}
	if age == "" {
		age = fallback
	}

	// Several at once is the parallel-batch case. Naming them all matters more
	// than brevity here: "3 tools" tells you nothing about which one is slow.
	if len(names) == 1 {
		return "⟳ " + names[0] + age
	}
	const maxNamed = 4
	if len(names) > maxNamed {
		return fmt.Sprintf("⟳ %s +%d more%s", strings.Join(names[:maxNamed], ", "), len(names)-maxNamed, age)
	}
	return "⟳ " + strings.Join(names, ", ") + age
}
