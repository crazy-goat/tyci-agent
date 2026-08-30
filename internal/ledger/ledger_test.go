package ledger

import (
	"os"
	"testing"

	"github.com/decodo/tyci/internal/pricing"
	"github.com/decodo/tyci/stream"
)

func TestCost_SplitsCacheFromFreshInput(t *testing.T) {
	r := pricing.Rates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
	// 100k input of which 90k was cached, 10k output, 20k cache writes.
	u := stream.Usage{Input: 100_000, CacheRead: 90_000, CacheWrite: 20_000, Output: 10_000}
	got := Cost(r, u)
	want := 0.010*3 + 0.090*0.3 + 0.020*3.75 + 0.010*15
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("Cost = %v, want %v", got, want)
	}
}

// A provider with no separate cache pricing bills cached tokens at the input
// rate — "no cache discount", not "free".
func TestCost_FallsBackToInputRateForCache(t *testing.T) {
	r := pricing.Rates{Input: 2, Output: 6}
	u := stream.Usage{Input: 1_000_000, CacheRead: 1_000_000}
	if got := Cost(r, u); got != 2 {
		t.Fatalf("Cost = %v, want 2", got)
	}
}

func TestCost_UnknownRatesIsZero(t *testing.T) {
	if got := Cost(pricing.Rates{}, stream.Usage{Input: 1_000_000}); got != 0 {
		t.Fatalf("Cost = %v, want 0 for unknown rates", got)
	}
}

func TestRecord_SplitsMainFromSubagentAndAccumulates(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	u := stream.Usage{Input: 1000, Output: 100}
	Record(Main, "p", "m", "", u)
	Record(Main, "p", "m", "", u)
	Record(Subagent, "p", "m", "", u)

	s := Get()
	if len(s.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (main + subagent for the same model)", len(s.Rows))
	}
	if s.Rows[0].Runs != 2 || s.Rows[0].Usage.Input != 2000 {
		t.Fatalf("main row = %+v, want 2 runs / 2000 input", s.Rows[0])
	}
	if s.Main.Output != 200 || s.Sub.Output != 100 {
		t.Fatalf("main/sub totals = %d/%d, want 200/100", s.Main.Output, s.Sub.Output)
	}
}

// TestKind_ScoutIsDistinctFromSubagent pins item 21 round B's addition: a
// scout's spend must be countable on its own (ScoutUSD), while its token
// usage still rolls up into Sub alongside ordinary subagents — a scout is a
// crippled subagent, and the token-side rollup treats it as one; only the
// dollar figure gets its own line, because telling scout cost apart from
// subagent cost is the whole point of the split.
func TestKind_ScoutIsDistinctFromSubagent(t *testing.T) {
	if got := Scout.String(); got != "scout" {
		t.Fatalf("Scout.String() = %q, want %q", got, "scout")
	}

	Reset()
	t.Cleanup(Reset)
	u := stream.Usage{Input: 1000, Output: 100}
	Record(Subagent, "p", "m", "job-1", u)
	Record(Scout, "p", "m", "job-2", u)

	s := Get()
	if s.Sub.Output != 200 {
		t.Fatalf("Sub.Output = %d, want 200 (subagent + scout combined)", s.Sub.Output)
	}
	if s.Unpriced != 2 {
		t.Fatalf("Unpriced = %d, want 2 ('p'/'m' has no catalog price)", s.Unpriced)
	}
	// Both rows are unpriced ("p"/"m" is not a real catalog entry), so
	// SubagentUSD/ScoutUSD/TotalUSD all stay at their zero value here — this
	// test is about which BUCKET a row would land in, not the dollar
	// arithmetic itself (see TestGet_ScoutUSDCountsOnItsOwn for that).
	if s.TotalUSD() != s.MainUSD+s.SubagentUSD+s.ScoutUSD {
		t.Fatalf("TotalUSD must equal Main+Subagent+Scout, got %v", s)
	}
}

// TestGet_ScoutUSDCountsOnItsOwn proves ScoutUSD accumulates independently
// of SubagentUSD once a row is actually priced — the case
// TestKind_ScoutIsDistinctFromSubagent above could not exercise, since both
// its rows were unpriced.
func TestGet_ScoutUSDCountsOnItsOwn(t *testing.T) {
	// A real, priced catalog entry — via Record, not by poking the package's
	// unexported rows/order globals directly — so this exercises the actual
	// write path (Record -> pricing.Lookup -> Cost) instead of asserting
	// against a hand-built Row that could drift from what Record would ever
	// produce. writeTestCatalog mirrors display/tui_tokens_test.go's helper
	// of the same name (that one lives in a different package, so it can't
	// be reused directly).
	dir := t.TempDir()
	writeTestCatalog(t, dir, `{"p":{"id":"p","models":{"m":{"id":"m","name":"m","cost":{"input":1,"output":1}}}}}`)
	t.Setenv("HOME", dir)
	pricing.Reset()
	t.Cleanup(pricing.Reset)

	Reset()
	t.Cleanup(Reset)
	// 1,000,000 input @ $1/M + 1,500,000 output @ $1/M = $2.5.
	Record(Scout, "p", "m", "j", stream.Usage{Input: 1_000_000, Output: 1_500_000})

	s := Get()
	if s.ScoutUSD != 2.5 {
		t.Fatalf("ScoutUSD = %v, want 2.5", s.ScoutUSD)
	}
	if s.SubagentUSD != 0 {
		t.Fatalf("SubagentUSD = %v, want 0 (this row is a Scout, not a Subagent)", s.SubagentUSD)
	}
	if s.TotalUSD() != 2.5 {
		t.Fatalf("TotalUSD = %v, want 2.5", s.TotalUSD())
	}
}

// writeTestCatalog seeds a minimal models.dev-shaped provider catalog under
// homeDir/.tyci/providers.json — mirrors display/tui_tokens_test.go's
// helper of the same name (different package, so not directly reusable).
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

// TestByModel_AggregatesAcrossJobIDs covers the regression a per-job Row key
// introduced: Get().Rows now has one row per {Kind,Provider,Model,JobID}, so
// twelve subagents on the same model would otherwise render as twelve
// identical-looking lines in the Tokens tab instead of one. ByModel must
// collapse them back down to one row per {Kind,Provider,Model}, summing
// usage/cost/runs across every job that used it.
func TestByModel_AggregatesAcrossJobIDs(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	u := stream.Usage{Input: 1000, Output: 100}
	Record(Main, "p", "m", "", u)
	Record(Subagent, "p", "m", "job-1", u)
	Record(Subagent, "p", "m", "job-2", u)
	Record(Subagent, "p", "m", "job-3", u)

	// Sanity check: the per-job view really did split into 4 rows.
	if got := len(Get().Rows); got != 4 {
		t.Fatalf("Get().Rows = %d, want 4 (1 main + 3 per-job subagent rows)", got)
	}

	byModel := ByModel()
	if len(byModel) != 2 {
		t.Fatalf("ByModel() = %d rows, want 2 (one main, one subagent, collapsed across job ids): %+v", len(byModel), byModel)
	}
	var main, sub Row
	for _, r := range byModel {
		if r.Kind == Main {
			main = r
		} else {
			sub = r
		}
	}
	if main.Runs != 1 || main.Usage.Input != 1000 {
		t.Fatalf("main row = %+v, want 1 run / 1000 input", main)
	}
	if sub.Runs != 3 || sub.Usage.Input != 3000 {
		t.Fatalf("subagent row = %+v, want 3 runs / 3000 input summed across job ids", sub)
	}
}

// TestByModel_UnpricedPropagatesAcrossJobIDs: if any one of several jobs on
// the same model is unpriced, the aggregated row must report unpriced too —
// silently averaging a known price with an unknown one would understate the
// bill, the one thing this package exists not to do.
func TestByModel_UnpricedPropagatesAcrossJobIDs(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	Record(Subagent, "nope", "no-such-model", "job-1", stream.Usage{Input: 10})
	Record(Subagent, "nope", "no-such-model", "job-2", stream.Usage{Input: 20})

	byModel := ByModel()
	if len(byModel) != 1 {
		t.Fatalf("expected 1 aggregated row, got %d", len(byModel))
	}
	if byModel[0].Priced {
		t.Fatalf("expected the aggregated row to stay unpriced, got Priced=true")
	}
}

// An unpriced model must not contribute $0 to the total silently — it has to
// be visible as "we don't know".
func TestGet_UnpricedRowsCounted(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	Record(Main, "nope", "no-such-model", "", stream.Usage{Input: 500})
	s := Get()
	if s.Unpriced != 1 {
		t.Fatalf("Unpriced = %d, want 1", s.Unpriced)
	}
	if s.TotalUSD() != 0 {
		t.Fatalf("TotalUSD = %v, want 0 when nothing is priced", s.TotalUSD())
	}
}

func TestRecord_IgnoresEmptyUsage(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	Record(Main, "p", "m", "", stream.Usage{})
	if len(Get().Rows) != 0 {
		t.Fatal("an all-zero usage should not create a row")
	}
}

func TestReset_ClearsEverything(t *testing.T) {
	Reset()
	Record(Main, "p", "m", "", stream.Usage{Input: 10})
	Reset()
	if s := Get(); len(s.Rows) != 0 || s.TotalUSD() != 0 {
		t.Fatalf("after Reset: %+v, want empty", s)
	}
}
