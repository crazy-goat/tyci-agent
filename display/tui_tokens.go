package display

import (
	"fmt"
	"strings"

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
func (m TuiModel) buildContextCost() string {
	var parts []string

	used, limit, ok := m.contextUsed()
	switch {
	case ok:
		// Absolute first, share of the window after it: the token count is
		// what you compare against yesterday, the percentage is what tells
		// you how close the wall is.
		parts = append(parts, fmt.Sprintf("ctx %s (%d%%)", fmtTokens(used), used*100/limit))
	case used > 0:
		// No published limit: the absolute number is still useful.
		parts = append(parts, "ctx "+fmtTokens(used))
	}

	snap := ledger.Get()
	if cost := formatCost(snap); cost != "" {
		parts = append(parts, cost)
	}
	return strings.Join(parts, ", ")
}

// formatCost renders the session bill. Delegated work is called out
// separately when there is any, because a surprising total is nearly always
// children. An unpriced model makes the total a lower bound, and says so with
// a "+?" rather than quietly under-reporting.
func formatCost(snap ledger.Snapshot) string {
	total := snap.TotalUSD()
	if total == 0 {
		if snap.Unpriced == 0 {
			return ""
		}
		// Tokens were spent on a model the catalog does not price. "0.00$"
		// would read as "almost free"; the truth is that we do not know.
		return "?$"
	}
	s := fmtUSD(total) + "$"
	if snap.SubagentUSD > 0 {
		s += " (sub " + fmtUSD(snap.SubagentUSD) + "$)"
	}
	if snap.Unpriced > 0 {
		s += "+?"
	}
	return s
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
	// rest goes to the label, clamped so a very wide sidebar doesn't stretch
	// it absurdly and a very narrow one still gets a usable label instead of
	// collapsing to nothing.
	labelWidth := width - 17
	if labelWidth < 4 {
		labelWidth = 4
	}
	if labelWidth > 22 {
		labelWidth = 22
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
			// `provider refresh` mid-session) had no catalog price, not
			// that r.USD itself is worthless. Showing a flat "$?" here
			// used to throw away a real, known dollar figure; this mirrors
			// formatCost's own "$1.23+?" convention (tui_status.go) for a
			// partially-priced total instead.
			var cost string
			switch {
			case r.Priced:
				cost = "$" + fmtUSD(r.USD)
			case r.USD > 0:
				cost = "$" + fmtUSD(r.USD) + "+?"
			default:
				cost = "$?"
			}
			label := r.Model
			if r.Kind == ledger.Subagent {
				label = "↳ " + label
			}
			out = append(out, fmt.Sprintf("  %-*s %5s %s", labelWidth,
				truncateRunes(label, labelWidth), fmtTokens(r.Usage.Input+r.Usage.Output), cost))
		}
		snap := ledger.Get()
		out = append(out, fmt.Sprintf("  %-*s %5s $%s", labelWidth, "total", "",
			fmtUSD(snap.TotalUSD())))
		if snap.SubagentUSD > 0 {
			out = append(out, fmt.Sprintf("  %-*s %5s $%s", labelWidth, "of that delegated", "",
				fmtUSD(snap.SubagentUSD)))
		}
	}

	// Warn only about a model actually in this session, and only when its
	// provider has priced nothing at all — a model that reads $0 while its
	// provider prices other models is presumed genuinely free, not missing
	// data, and must not trigger this.
	for _, r := range byModel {
		if !r.Priced && pricing.ProviderNeedsPrices(r.Provider) {
			out = append(out, "", "no prices for "+r.Provider+" —", "run `tyci provider refresh`")
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
