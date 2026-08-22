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

	snap := ledger.Get()
	if len(snap.Rows) > 0 {
		out = append(out, "", "session")
		for _, r := range snap.Rows {
			cost := "$" + fmtUSD(r.USD)
			if !r.Priced {
				cost = "$?"
			}
			label := r.Model
			if r.Kind == ledger.Subagent {
				label = "↳ " + label
			}
			out = append(out, fmt.Sprintf("  %-22s %5s %s",
				truncateRunes(label, 22), fmtTokens(r.Usage.Input+r.Usage.Output), cost))
		}
		out = append(out, fmt.Sprintf("  %-22s %5s $%s", "total", "",
			fmtUSD(snap.TotalUSD())))
		if snap.SubagentUSD > 0 {
			out = append(out, fmt.Sprintf("  %-22s %5s $%s", "of that delegated", "",
				fmtUSD(snap.SubagentUSD)))
		}
	}

	// Warn only about a model actually in this session (snap.Rows), and only
	// when its provider has priced nothing at all — a model that reads $0
	// while its provider prices other models is presumed genuinely free, not
	// missing data, and must not trigger this.
	for _, r := range snap.Rows {
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
