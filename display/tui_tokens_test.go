package display

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/internal/pricing"
	"github.com/decodo/tyci/stream"
)

func TestFmtTokens(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0", "0"}, {"999", "999"}, {"1500", "1.5k"},
		{"68000", "68k"}, {"1500000", "1.5M"},
	}
	nums := []int{0, 999, 1500, 68000, 1500000}
	for i, c := range cases {
		if got := fmtTokens(nums[i]); got != c.want {
			t.Errorf("fmtTokens(%d) = %q, want %q", nums[i], got, c.want)
		}
	}
}

func TestFmtUSD_PrecisionByMagnitude(t *testing.T) {
	if got := fmtUSD(0.0034); got != "0.003" {
		t.Errorf("small bill = %q, want 0.003", got)
	}
	if got := fmtUSD(12.345); got != "12.35" {
		t.Errorf("mid bill = %q, want 12.35", got)
	}
	if got := fmtUSD(1234.6); got != "1235" {
		t.Errorf("large bill = %q, want 1235", got)
	}
}

// Unpriced tokens must never render as a dollar amount — "$0.00" would read
// as almost free.
func TestFormatCost_UnpricedShowsQuestionMark(t *testing.T) {
	got := formatCost(ledger.Snapshot{Unpriced: 1})
	if got != "?$" {
		t.Fatalf("formatCost = %q, want ?$", got)
	}
}

func TestFormatCost_EmptyWhenNothingSpent(t *testing.T) {
	if got := formatCost(ledger.Snapshot{}); got != "" {
		t.Fatalf("formatCost = %q, want empty", got)
	}
}

func TestFormatCost_CallsOutDelegatedSpend(t *testing.T) {
	got := formatCost(ledger.Snapshot{MainUSD: 1, SubagentUSD: 2})
	if !strings.Contains(got, "3.00$") || !strings.Contains(got, "sub 2.00$") {
		t.Fatalf("formatCost = %q, want total and subagent share", got)
	}
}

// A partially-priced session reports a lower bound, and marks it as such.
func TestFormatCost_MarksLowerBound(t *testing.T) {
	got := formatCost(ledger.Snapshot{MainUSD: 1.5, Unpriced: 2})
	if !strings.HasSuffix(got, "+?") {
		t.Fatalf("formatCost = %q, want a +? suffix", got)
	}
}

// With no catalog on this machine the limit is unknown, so the bar shows the
// absolute context size rather than a percentage of nothing.
func TestBuildContextCost_NoLimitShowsAbsolute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pricing.Reset()
	ledger.Reset()
	t.Cleanup(pricing.Reset)
	t.Cleanup(ledger.Reset)
	m := TuiModel{modelName: "no-such-model", lastUsage: stream.Usage{Input: 68000, Output: 500}}
	got := m.buildContextCost()
	if !strings.Contains(got, "ctx 68k") {
		t.Fatalf("buildContextCost = %q, want an absolute ctx figure", got)
	}
	if strings.Contains(got, "%") {
		t.Fatalf("buildContextCost = %q, should not show a percentage without a limit", got)
	}
}

func TestBuildContextCost_EmptyBeforeFirstTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pricing.Reset()
	ledger.Reset()
	t.Cleanup(pricing.Reset)
	t.Cleanup(ledger.Reset)
	m := TuiModel{modelName: "m"}
	if got := m.buildContextCost(); got != "" {
		t.Fatalf("buildContextCost = %q, want empty before any usage", got)
	}
}

// The detail view is what the status bar gave up: per-turn breakdown, timings
// and the per-model session table.
func TestBuildUsageDetail_HasTurnAndSessionSections(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pricing.Reset()
	ledger.Reset()
	t.Cleanup(pricing.Reset)
	t.Cleanup(ledger.Reset)
	ledger.Record(ledger.Main, "p", "m", "", stream.Usage{Input: 1000, Output: 100})
	ledger.Record(ledger.Subagent, "p", "m", "", stream.Usage{Input: 5000, Output: 200})

	m := TuiModel{modelName: "m", lastUsage: stream.Usage{Input: 1000, Output: 100, CacheRead: 400}}
	lines := strings.Join(m.buildUsageDetail(40), "\n")

	for _, want := range []string{"last turn", "in=600", "tok/s", "session", "↳ m", "total"} {
		if !strings.Contains(lines, want) {
			t.Errorf("detail missing %q:\n%s", want, lines)
		}
	}
}

// A stripped catalog is the common case after an upgrade; say what fixes it.
// The hint is about the model actually in this session, not the whole
// catalog: it must appear when that session used a provider that prices
// nothing at all...
func TestBuildUsageDetail_HintsAtRefreshWhenUnpriced(t *testing.T) {
	dir := t.TempDir()
	writeTestCatalog(t, dir, `{"unpriced":{"id":"unpriced","models":{"m":{"id":"m","name":"m"}}}}`)
	t.Setenv("HOME", dir)
	pricing.Reset()
	ledger.Reset()
	t.Cleanup(pricing.Reset)
	t.Cleanup(ledger.Reset)

	ledger.Record(ledger.Main, "unpriced", "m", "", stream.Usage{Input: 10, Output: 10})

	m := TuiModel{modelName: "m"}
	lines := strings.Join(m.buildUsageDetail(40), "\n")
	if !strings.Contains(lines, "provider refresh") {
		t.Fatalf("detail should suggest a catalog refresh:\n%s", lines)
	}
}

// ...and must not appear for a model that reads $0 while its own provider
// prices other models — that is presumed genuinely free, not missing data.
func TestBuildUsageDetail_NoHintForGenuinelyFreeModel(t *testing.T) {
	dir := t.TempDir()
	writeTestCatalog(t, dir, `{"mixed":{"id":"mixed","models":{
		"priced":{"id":"priced","name":"priced","cost":{"input":3,"output":15}},
		"free":{"id":"free","name":"free"}
	}}}`)
	t.Setenv("HOME", dir)
	pricing.Reset()
	ledger.Reset()
	t.Cleanup(pricing.Reset)
	t.Cleanup(ledger.Reset)

	ledger.Record(ledger.Main, "mixed", "free", "", stream.Usage{Input: 10, Output: 10})

	m := TuiModel{modelName: "free"}
	lines := strings.Join(m.buildUsageDetail(40), "\n")
	if strings.Contains(lines, "provider refresh") {
		t.Fatalf("a genuinely free model must not trigger the refresh hint:\n%s", lines)
	}
}

// The discriminating case this item exists to fix: provider A is priced,
// provider B is not, and the session's usage is on B. The old global check
// (one priced provider anywhere silences the warning forever, the nexos
// case) would stay silent here; the per-provider check must still warn,
// because it is B — the provider actually in use — that has no prices.
func TestBuildUsageDetail_HintsWhenUsedProviderUnpricedEvenIfAnotherProviderIsPriced(t *testing.T) {
	dir := t.TempDir()
	writeTestCatalog(t, dir, `{
		"priced-provider":{"id":"priced-provider","models":{
			"p":{"id":"p","name":"p","cost":{"input":3,"output":15}}
		}},
		"unpriced-provider":{"id":"unpriced-provider","models":{
			"u":{"id":"u","name":"u"}
		}}
	}`)
	t.Setenv("HOME", dir)
	pricing.Reset()
	ledger.Reset()
	t.Cleanup(pricing.Reset)
	t.Cleanup(ledger.Reset)

	// The session's usage is on the unpriced provider, not the priced one.
	ledger.Record(ledger.Main, "unpriced-provider", "u", "", stream.Usage{Input: 10, Output: 10})

	m := TuiModel{modelName: "u"}
	lines := strings.Join(m.buildUsageDetail(40), "\n")
	if !strings.Contains(lines, "provider refresh") {
		t.Fatalf("a priced provider elsewhere in the catalog must not silence the hint for the provider actually in use:\n%s", lines)
	}
}

func writeTestCatalog(t *testing.T, homeDir, body string) {
	t.Helper()
	dir := homeDir + "/.tyci"
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/providers.json", []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestBuildUsageDetail_AggregatesSameModelAcrossManySubagents guards the
// regression a per-job ledger key introduced: many subagents sharing a
// model must render as one aggregated session line, not one per job id.
func TestBuildUsageDetail_AggregatesSameModelAcrossManySubagents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pricing.Reset()
	ledger.Reset()
	t.Cleanup(pricing.Reset)
	t.Cleanup(ledger.Reset)

	for i := 0; i < 12; i++ {
		ledger.Record(ledger.Subagent, "p", "m", fmt.Sprintf("job-%d", i), stream.Usage{Input: 100, Output: 10})
	}

	m := TuiModel{modelName: "m"}
	lines := m.buildUsageDetail(40)
	count := 0
	for _, l := range lines {
		if strings.Contains(l, "↳ m") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 aggregated line for 12 same-model subagents, got %d:\n%s", count, strings.Join(lines, "\n"))
	}
}

// TestBuildUsageDetail_NarrowWidthKeepsRowsIntact covers the sidebar's
// actual inner width (as low as ~34 columns): the per-model row must adapt
// its label column instead of the fixed 22-column format truncating badly
// or overflowing the line.
func TestBuildUsageDetail_NarrowWidthKeepsRowsIntact(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pricing.Reset()
	ledger.Reset()
	t.Cleanup(pricing.Reset)
	t.Cleanup(ledger.Reset)

	ledger.Record(ledger.Main, "p", "a-fairly-long-model-name-here", "", stream.Usage{Input: 100, Output: 10})

	// 30 is comfortably below the sidebar's real floor (contentWidth is
	// never under ~34 — see sidebarLayout), but still narrow enough to prove
	// the row adapts rather than assuming the old fixed 22-column label.
	const width = 30
	m := TuiModel{modelName: "m"}
	for _, l := range m.buildUsageDetail(width) {
		if lipgloss.Width(l) > width {
			t.Errorf("line %q is %d columns wide, want <= %d", l, lipgloss.Width(l), width)
		}
	}
}

// TestBuildUsageDetail_PartiallyPricedRowKeepsKnownDollars covers the
// review finding that ByModel's aggregate lost real, known dollars: when
// the same model is recorded once before a catalog refresh (unpriced) and
// once after (priced) — two different jobs, same model — the aggregated
// row's Priced flag is correctly false (not FULLY priced), but its USD
// still holds the known-priced job's real cost. Rendering that as a flat
// "$?" discarded it; it must render as "$<amount>+?" instead, mirroring
// formatCost's own convention for a partially-priced total.
func TestBuildUsageDetail_PartiallyPricedRowKeepsKnownDollars(t *testing.T) {
	unprocedDir := t.TempDir()
	t.Setenv("HOME", unprocedDir)
	pricing.Reset()
	ledger.Reset()
	t.Cleanup(pricing.Reset)
	t.Cleanup(ledger.Reset)

	// job-1 recorded while the catalog has no price for "m" at all.
	ledger.Record(ledger.Subagent, "p", "m", "job-1", stream.Usage{Input: 1_000_000, Output: 100_000})

	// Simulate a `tyci provider refresh`: the catalog now prices "m".
	pricedDir := t.TempDir()
	writeTestCatalog(t, pricedDir, `{"p":{"id":"p","models":{"m":{"id":"m","name":"m","cost":{"input":3,"output":15}}}}}`)
	t.Setenv("HOME", pricedDir)
	pricing.Reset()

	// job-2, same model, recorded after the refresh — this one prices
	// cleanly.
	ledger.Record(ledger.Subagent, "p", "m", "job-2", stream.Usage{Input: 1_000_000, Output: 100_000})

	byModel := ledger.ByModel()
	if len(byModel) != 1 {
		t.Fatalf("expected 1 aggregated row, got %d: %+v", len(byModel), byModel)
	}
	row := byModel[0]
	if row.Priced {
		t.Fatalf("expected the aggregate to be marked not-fully-priced, got Priced=true")
	}
	if row.USD <= 0 {
		t.Fatalf("expected the aggregate to still carry job-2's known cost, got USD=%v", row.USD)
	}

	m := TuiModel{modelName: "m"}
	lines := strings.Join(m.buildUsageDetail(40), "\n")
	if strings.Contains(lines, "$?") {
		t.Fatalf("expected the known dollar amount to survive, not a flat \"$?\":\n%s", lines)
	}
	if !strings.Contains(lines, "+?") {
		t.Fatalf("expected the partially-priced marker \"+?\" on the aggregated row:\n%s", lines)
	}
}

func TestTruncateRunes_KeepsRunesIntact(t *testing.T) {
	if got := truncateRunes("ąćęłńóśźż", 4); got != "ąćę…" {
		t.Fatalf("truncateRunes = %q, want ąćę…", got)
	}
	if got := truncateRunes("short", 10); got != "short" {
		t.Fatalf("truncateRunes shortened an already-short string: %q", got)
	}
}
