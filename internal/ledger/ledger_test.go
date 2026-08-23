package ledger

import (
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
