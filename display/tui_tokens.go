package display

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/internal/pricing"
)

// contextUsed is how much of the model's context window the last turn
// occupied, as a fraction. The last turn's Input is the right measure: it is
// the whole conversation the provider was just sent, cached parts included,
// which is exactly what will overflow. Returns ok=false when the catalog does
// not know the model's limit, since a percentage of an unknown is nonsense.
func (m TuiModel) contextUsed() (used, limit int, ok bool) {
	used = m.lastUsage.Input + m.lastUsage.Output
	_, lim := pricing.Lookup("", m.modelName)
	if lim.Context <= 0 {
		return used, 0, false
	}
	return used, lim.Context, true
}

// buildContextCost is the right-hand side of the status bar: how full the
// context is, and what the session has cost including delegated work.
//
// Everything else that used to live here — per-turn input/output/cache
// breakdown, timings, throughput — is in the sidebar's Tokens tab, one click
// away on the context figure.
//
// The whole return value is bounded to at most m.width-1 columns (see
// rightBudget below) — not just the cost half of it. buildStatus
// (tui_status.go) truncates the LEFT side of the status bar against
// however wide THIS turns out to be, but never truncates this side itself;
// an unbounded right therefore forces lipgloss to WRAP the whole status row
// instead of clipping it, exactly the failure mode buildStatus's own
// comment documents for an unbounded left ("a single ~106-char message
// turned a 20-line frame into 20+ lines"). The -1 reserve is not
// decorative: buildStatus's maxLeftW clamps to a floor of 1 column even
// when the right side leaves no room at all, so the right side must leave
// at least that 1 column free or left+right together exceed m.width by
// exactly the amount the floor forced — measured, not assumed: at
// rightW == m.width, leftW's forced-1 pushes the total to m.width+1.
func (m TuiModel) buildContextCost() string {
	// rightBudget bounds this function's ENTIRE output. m.width <= 0 (no
	// resize has happened yet) is treated as "don't know", not "zero room":
	// this renders unbounded, same as before this budget existed — a real
	// terminal always resizes to a positive width before the first frame,
	// so this only matters for a TuiModel built directly in a test.
	rightBudget := math.MaxInt
	if m.width > 0 {
		rightBudget = m.width - 1
		if rightBudget < 0 {
			rightBudget = 0
		}
	}

	var parts []string

	used, limit, ok := m.contextUsed()
	var ctxPart string
	switch {
	case ok:
		// Absolute first, share of the window after it: the token count is
		// what you compare against yesterday, the percentage is what tells
		// you how close the wall is.
		ctxPart = fmt.Sprintf("ctx %s (%d%%)", fmtTokens(used), used*100/limit)
	case used > 0:
		// No published limit: the absolute number is still useful.
		ctxPart = "ctx " + fmtTokens(used)
	}
	// ctxPart itself has never been width-bounded (this predates the scout
	// kind), but rightBudget now makes that a documented invariant instead
	// of an accident: a ctxPart wider than the whole right-side budget
	// cannot be shown at all without wrapping the row on its own, so it is
	// dropped rather than rendered — this is the one case nothing later
	// (formatCost's own fitting) can rescue, since there is no fallback
	// shorter than "nothing" for the context figure.
	if ctxPart != "" && lipgloss.Width(ctxPart) > rightBudget {
		ctxPart = ""
	}
	if ctxPart != "" {
		parts = append(parts, ctxPart)
	}

	snap := ledger.Get()
	// costBudget is whatever rightBudget has left after ctxPart and its
	// ", " separator (only spent when ctxPart survived the check above).
	costBudget := rightBudget
	if ctxPart != "" {
		costBudget -= lipgloss.Width(ctxPart) + 2 // ", " separator
		if costBudget < 0 {
			costBudget = 0
		}
	}
	if cost := formatCost(snap, costBudget); cost != "" {
		parts = append(parts, cost)
	}
	return strings.Join(parts, ", ")
}

// formatCost renders the session bill for the status bar — buildContextCost
// is its only caller (the Tokens tab, buildUsageDetail below, builds its own
// per-model breakdown independently). Delegated work is called out
// separately when there is any, because a surprising total is nearly always
// children — scouts get their own "(scout ...)" figure alongside "(sub ...)"
// rather than folding into it, so a burst of scout calls does not hide
// inside what looks like an ordinary subagent bill. Unpriced models
// contribute $0.00 — the owner's explicit choice (2026-08-23), reversing the
// old "+?" lower-bound marker: the ledger still tracks unpriced rows, so the
// figure stays available to anything that wants it, but the UI no longer
// decorates the cost with it.
//
// maxWidth caps the rendered string so the status bar's right side can
// never itself wrap the row (see buildContextCost's budget comment). When
// the full breakdown doesn't fit, the two parenthetical clauses collapse
// into one shared bracket first ("(sub X$ scout Y$)" instead of two
// separate ones) — cheaper than dropping one outright, since both figures
// stay visible on a merely-tight terminal. Only on a terminal too narrow
// even for that does the breakdown disappear, falling back to the bare
// total; and on a terminal too narrow even for the bare total, the whole
// cost figure is omitted rather than truncated mid-number, which would
// misrepresent the bill.
func formatCost(snap ledger.Snapshot, maxWidth int) string {
	total := snap.TotalUSD()
	fits := func(s string) bool { return lipgloss.Width(s) <= maxWidth }

	if total == 0 {
		if snap.Unpriced == 0 {
			return ""
		}
		// "$0.00" goes through the same fits check as every other rendered
		// form below — a caller squeezing maxWidth down for a narrow
		// terminal must not have this one branch bypass it (that bypass
		// was the bug: a session with only unpriced usage could render
		// "$0.00" past maxWidth with nothing else in this function ever
		// getting a chance to drop it).
		if fits("$0.00") {
			return "$0.00"
		}
		return ""
	}

	base := fmtUSD(total) + "$"
	full := base
	if snap.SubagentUSD > 0 {
		full += " (sub " + fmtUSD(snap.SubagentUSD) + "$)"
	}
	if snap.ScoutUSD > 0 {
		full += " (scout " + fmtUSD(snap.ScoutUSD) + "$)"
	}
	if fits(full) {
		return full
	}

	var bits []string
	if snap.SubagentUSD > 0 {
		bits = append(bits, "sub "+fmtUSD(snap.SubagentUSD)+"$")
	}
	if snap.ScoutUSD > 0 {
		bits = append(bits, "scout "+fmtUSD(snap.ScoutUSD)+"$")
	}
	if len(bits) > 0 {
		merged := base + " (" + strings.Join(bits, " ") + ")"
		if fits(merged) {
			return merged
		}
	}

	if fits(base) {
		return base
	}
	return ""
}

// fmtUSD keeps small bills legible: cents matter at $0.03, they do not at $12.
func fmtUSD(v float64) string {
	switch {
	case v == 0:
		return "0.00"
	case v < 1:
		return fmt.Sprintf("%.3f", v)
	case v < 100:
		return fmt.Sprintf("%.2f", v)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

// fmtTokens abbreviates token counts the way a person reads them: 68k, 1.2M.
func fmtTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// buildUsageDetail is the Tokens tab: everything the status bar used to show,
// plus the per-model breakdown the status bar never could.
//
// width sizes the per-model breakdown's label column so it reads cleanly at
// the sidebar's actual inner width (as narrow as ~34 columns) instead of a
// fixed 22-column label truncating badly (or, at a wider width, leaving the
// row needlessly cramped).
func (m TuiModel) buildUsageDetail(width int) []string {
	var out []string

	used, limit, ok := m.contextUsed()
	if ok {
		out = append(out, fmt.Sprintf("context  %s / %s (%d%%)",
			fmtTokens(used), fmtTokens(limit), used*100/limit))
	} else if used > 0 {
		out = append(out, "context  "+fmtTokens(used)+" (limit unknown)")
	}

	if m.lastUsage.Input > 0 || m.lastUsage.Output > 0 {
		out = append(out, "", "last turn", "  "+usageTokens(m.lastUsage), "  "+timingTokens(m.lastUsage, m.lastStats))
	}

	// labelWidth adapts the per-model breakdown's first column to the
	// available width. The row's literal overhead is 9 columns ("  " + the
	// space before the numeric field + the numeric field's own width + "$"),
	// plus up to ~8 more for the cost figure itself (e.g. "1234.56"); the
	// rest goes to the label. No upper clamp: a wide sidebar should spend
	// its extra width on the model name (which is what actually gets
	// truncated in practice) rather than sitting unused, so this only
	// floors at a usable minimum for a narrow sidebar.
	labelWidth := width - 17
	if labelWidth < 4 {
		labelWidth = 4
	}

	// ByModel, not Get().Rows: Row's key now includes a job id (so the
	// Subagents tab can track a child's tokens separately), which would
	// otherwise show N identical-looking lines for N subagents that happen
	// to share a model instead of one aggregated line — see ByModel's doc
	// comment.
	byModel := ledger.ByModel()
	if len(byModel) > 0 {
		out = append(out, "", "session")
		for _, r := range byModel {
			// r.USD is already the known-priced sum only (Record/Cost give
			// an unpriced call $0, so it never inflates this) — !r.Priced
			// means at least one constituent row (e.g. before vs. after a
			// `provider refresh` mid-session) had no catalog price. The
			// old "$?"/"+?" markers were dropped by decision (2026-08-23):
			// every row renders a plain dollar figure.
			cost := "$" + fmtUSD(r.USD)
			label := r.Model
			switch r.Kind {
			case ledger.Subagent:
				label = "↳ " + label
			case ledger.Scout:
				label = "↳scout " + label
			}
			out = append(out, fmt.Sprintf("  %-*s %5s %s", labelWidth,
				truncateRunes(label, labelWidth), fmtTokens(r.Usage.Input+r.Usage.Output), cost))
		}
		// Token totals, unlike cost, are a plain sum across models here —
		// this "total" row is the one place tokens are allowed to add up
		// across models, since it is a raw count of everything the session
		// spent, not a quantity meant to compare one model's tokens against
		// another's (see item 1's Subagents-tab tokens-don't-roll-up note,
		// which is about comparing rows, not this whole-session sum).
		var totalTokens, delegatedTokens, scoutTokens int
		for _, r := range byModel {
			n := r.Usage.Input + r.Usage.Output
			totalTokens += n
			if r.Kind == ledger.Subagent || r.Kind == ledger.Scout {
				delegatedTokens += n
			}
			if r.Kind == ledger.Scout {
				scoutTokens += n
			}
		}
		snap := ledger.Get()
		out = append(out, fmt.Sprintf("  %-*s %5s $%s", labelWidth, "total",
			fmtTokens(totalTokens), fmtUSD(snap.TotalUSD())))
		// "of that delegated" is subagents and scouts combined — the token
		// figure above already sums both kinds, so the dollar figure next to
		// it must too, or the two numbers on the same line would describe
		// different populations. Scout gets its own separate line only when
		// non-zero, mirroring formatCost's status-bar treatment above.
		if delegated := snap.SubagentUSD + snap.ScoutUSD; delegated > 0 {
			out = append(out, fmt.Sprintf("  %-*s %5s $%s", labelWidth, "of that delegated",
				fmtTokens(delegatedTokens), fmtUSD(delegated)))
		}
		if snap.ScoutUSD > 0 {
			out = append(out, fmt.Sprintf("  %-*s %5s $%s", labelWidth, "  of that scout",
				fmtTokens(scoutTokens), fmtUSD(snap.ScoutUSD)))
		}
	}

	// Warn only about a model actually in this session, and only when its
	// provider has priced nothing at all — a model that reads $0 while its
	// provider prices other models is presumed genuinely free, not missing
	// data, and must not trigger this.
	for _, r := range byModel {
		if !r.Priced && pricing.ProviderNeedsPrices(r.Provider) {
			out = append(out, "", "no prices for "+r.Provider+" —", "run `tyci provider refresh`",
				"(if it's local/hand-added,", "edit providers.json instead)")
			break
		}
	}
	return out
}

// truncateRunes shortens s to at most n runes, marking the cut. Rune-based on
// purpose: model names carry non-ASCII often enough, and byte slicing would
// leave a broken sequence on screen.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}
