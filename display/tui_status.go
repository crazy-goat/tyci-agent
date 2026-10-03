package display

import (
	"fmt"
	"math"
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
		case "sending":
			leftParts = append(leftParts, "⟳ sending request..."+elapsedSuffix)
		case "waiting":
			leftParts = append(leftParts, "⟳ waiting for response..."+elapsedSuffix)
		case "thinking":
			leftParts = append(leftParts, "⟳ thinking..."+elapsedSuffix+m.throughputSuffix())
		case "responding":
			leftParts = append(leftParts, "⟳ responding..."+elapsedSuffix+m.throughputSuffix())
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

	return assembleStatusRow(strings.Join(leftParts, statusSep), strings.Join(rightParts, statusSep), m.width)
}

// statusSep joins the items of both sides of the status bar. The right side
// is also SPLIT on it by fitStatusRight, which drops items whole, so an item
// must be neither empty nor contain the separator itself: a doubled separator
// makes a drop leave the " │ " that preceded the missing item behind, and a
// leading one renders as an item of its own. buildStatus only appends
// non-empty items, so neither can happen today.
const statusSep = " │ "

// statusRightReserve is 1, which is the tight bound — not a margin.
//
// The right side may therefore fill width-1 columns, and the row still fits:
// the left side floors at 1 column (assembleStatusRow never truncates it to
// nothing), so left+right is then exactly `width`, rendered without either
// surrounding space. One column less and a figure that used to be shown
// disappears at every width where the right side would have been exactly
// width-1 wide; one column more and the two sides' floors push the row one
// column past the terminal.
//
// 1 is also what the shipped code used before #125 clamped the right side in
// the bar, so keeping it renders every session exactly as v0.1.0 did.
const statusRightReserve = 1

// statusRightBudget is how many columns the right side of the status bar may
// occupy at a given terminal width.
//
// ONE budget, defined next to the code that has to honour it last:
// assembleStatusRow. A producer sizes its own output with this function
// rather than with arithmetic of its own, because two budgets in two files
// drift apart silently and the TIGHTER one wins — which is the bug in #153:
// buildContextCost allowed width-1 while assembleStatusRow allowed width-3,
// so a session bill of "0.596$" was ellipsized to "0.59…" one layer above
// the rule that says a figure is dropped rather than cut.
func statusRightBudget(width int) int {
	switch {
	case width <= 0:
		// "not resized yet" — unbounded on purpose, matching what both
		// buildContextCost and assembleStatusRow did before this budget was
		// shared. That frame is built but never painted (paintRegion
		// returns early on width <= 0), so the only caller that can see it
		// is a test that builds a TuiModel directly.
		return math.MaxInt
	case width <= statusRightReserve:
		return 0
	default:
		return width - statusRightReserve
	}
}

// fitStatusRight returns the longest leading run of the right side's items
// that fits maxW columns. Whole items are dropped from the end rather than
// the run being ellipsized: the items are figures (a context percentage, a
// dollar amount), and half of one reads as the whole while stating less —
// which for a bill is worse than showing none.
//
// An item that does not fit on its own takes the rest with it: there is
// nothing shorter than "nothing" to show in its place, and cutting it is the
// thing this function exists to avoid.
func fitStatusRight(right string, maxW int) string {
	if right == "" || lipgloss.Width(right) <= maxW {
		return right
	}
	items := strings.Split(right, statusSep)
	kept := ""
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			continue // see statusSep: an item with nothing to show is not one,
			// and keeping the separator in front of it is what would
			// manufacture a trailing " │ "
		}
		candidate := item
		if kept != "" {
			candidate = kept + statusSep + item
		}
		if lipgloss.Width(candidate) > maxW {
			break
		}
		kept = candidate
	}
	return kept
}

// assembleStatusRow joins the left and right parts of the status bar into
// one row, clamping BOTH sides to the terminal width. Extracted from
// buildStatus so the width invariant can be tested with an over-long right
// part directly — buildStatus's only right-side producer (buildContextCost)
// already sizes itself with statusRightBudget, so an over-long right never
// reaches the bar through buildStatus alone.
func assembleStatusRow(left, right string, width int) string {
	// Clamp the right side in the bar itself, not just in each producer.
	// buildContextCost (the only current producer) honors the same budget,
	// but the protection would otherwise sit with the producer: any future
	// item appended to rightParts without it lets an over-long right string
	// through, forcing lipgloss to WRAP the row and drift the fixed frame
	// height — the same failure mode the left-side cap below exists to
	// prevent.
	//
	// Items are dropped, never cut, for the reason in fitStatusRight: a
	// cut figure misreports the session. width <= 0 ("no resize yet")
	// stays unbounded, matching statusRightBudget's documented contract;
	// that frame is never painted.
	if width > 0 {
		right = fitStatusRight(right, statusRightBudget(width))
	}

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
	maxLeftW := width - rightW - 3 // leading space + gap + trailing space
	if maxLeftW < 1 {
		maxLeftW = 1
	}
	left = truncateStatusText(left, maxLeftW)

	// Right-align the right part, with leading and trailing space.
	leftW := lipgloss.Width(left)
	padding := width - leftW - rightW
	if padding >= 2 {
		return " " + left + strings.Repeat(" ", padding-2) + right + " "
	}
	// Not enough room for both spaces; just show leading space.
	if padding >= 1 {
		return " " + left + strings.Repeat(" ", padding-1) + right
	}
	return left + right
}

// statusBarY returns the terminal row the status bar renders on: row 0 is
// the topbar, rows [1, messageRegionHeight()] are the fixed-height message
// region (padded to exactly that many rows — see buildMessageRegion), so the
// status bar always lands right after it regardless of what renders below
// (jobs panel, queue panel, file-complete popup, input).
func (m TuiModel) statusBarY() int {
	return 1 + m.messageRegionHeight()
}

// statusRightHit reports whether screen column x falls within the status
// bar's right-hand side (buildContextCost's rendered text plus its
// surrounding padding) — the "context figure" that opens the sidebar's
// Tokens tab on click. Deliberately generous (the whole trailing run of
// columns, not just the exact glyphs) rather than replicating buildStatus's
// padding arithmetic pixel-for-pixel: a slightly wider click target is a
// better trade than silently missing a click because of an off-by-one.
func (m TuiModel) statusRightHit(x int) bool {
	right := m.buildContextCost()
	if right == "" {
		return false
	}
	rightW := lipgloss.Width(right)
	// +2 for the padding buildStatus reserves before/after the right part.
	start := m.width - rightW - 2
	return x >= start
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

// trackDeltaBytes records n more bytes of thinking/text delta for the
// current round's cumulative throughput estimate (see throughputSuffix),
// remembering when the round's first delta arrived so the rate is averaged
// over the whole receiving phase instead of jittering per delta.
func (m *TuiModel) trackDeltaBytes(n int) {
	if m.roundFirstDeltaAt.IsZero() {
		m.roundFirstDeltaAt = time.Now()
	}
	m.roundBytes += n
}

// throughputSuffix returns " · ~N tok" (and, once enough time has passed to
// estimate a rate, " · ~N tok/s" as well) once the round has received its
// first delta, or "" before that. Tokens are estimated as bytes/4 (a common
// rule of thumb across providers — not exact, but a jittery precise count
// would be worse than a stable approximate one here).
//
// The two numbers answer different questions and so have different gates:
// the received count is exact-ish and useful immediately (item 56 — a
// "responding..." status with a frozen elapsed timer and no count at all
// gives no sense of progress), so it's shown from the very first delta. The
// rate needs enough wall time to not be wild ("~4000 tok/s" from one delta
// landing within the same millisecond it arrived), so it keeps the 0.5s
// floor and is averaged over the whole receiving phase since the round's
// FIRST delta — not the gap since the last one — so it settles instead of
// swinging between chunks.
func (m TuiModel) throughputSuffix() string {
	if m.roundFirstDeltaAt.IsZero() || m.roundBytes <= 0 {
		return ""
	}
	suffix := fmt.Sprintf(" · ~%d tok", m.roundBytes/4)
	elapsed := time.Since(m.roundFirstDeltaAt).Seconds()
	if elapsed < 0.5 {
		return suffix
	}
	tokPerSec := float64(m.roundBytes) / 4 / elapsed
	return suffix + fmt.Sprintf(" · ~%d tok/s", int(tokPerSec+0.5))
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
