package display

import (
	"os"
	"strings"
	"testing"

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
	ledger.Record(ledger.Main, "p", "m", stream.Usage{Input: 1000, Output: 100})
	ledger.Record(ledger.Subagent, "p", "m", stream.Usage{Input: 5000, Output: 200})

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

	ledger.Record(ledger.Main, "unpriced", "m", stream.Usage{Input: 10, Output: 10})

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

	ledger.Record(ledger.Main, "mixed", "free", stream.Usage{Input: 10, Output: 10})

	m := TuiModel{modelName: "free"}
	lines := strings.Join(m.buildUsageDetail(40), "\n")
	if strings.Contains(lines, "provider refresh") {
		t.Fatalf("a genuinely free model must not trigger the refresh hint:\n%s", lines)
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

func TestTruncateRunes_KeepsRunesIntact(t *testing.T) {
	if got := truncateRunes("ąćęłńóśźż", 4); got != "ąćę…" {
		t.Fatalf("truncateRunes = %q, want ąćę…", got)
	}
	if got := truncateRunes("short", 10); got != "short" {
		t.Fatalf("truncateRunes shortened an already-short string: %q", got)
	}
}
