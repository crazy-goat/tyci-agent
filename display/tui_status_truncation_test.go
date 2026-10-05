package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/crazy-goat/tyci-agent/internal/ledger"
	"github.com/crazy-goat/tyci-agent/internal/pricing"
	"github.com/crazy-goat/tyci-agent/stream"
)

// TestBuildStatus_LongStatusMessageIsTruncatedNotWrapped guards the item-27
// round-3 fix: buildStatus never capped m.statusMessage's width, and this bar
// is rendered as a single fixed-height row via lipgloss (tui_view.go), which
// WRAPS text wider than the terminal instead of clipping it. An uncapped
// left string therefore silently grew the rendered row into several lines,
// breaking the TUI's fixed-height layout — observed with a single ~106-char
// message against a 20-line frame.
//
// Two real-world sources of an overlong statusMessage motivated this: a
// confirmation echoing a job's Question verbatim (from a since-removed
// "/answer" command — the risk of an overlong echo remains, since nothing
// else caps m.statusMessage's length before it's set), and tui_keys.go's
// "/new has to wait — it changes the conversation this turn is writing to.
// Esc stops the turn, then press Enter." refusal (~110 chars) — both funnel
// through the exact same m.statusMessage field buildStatus reads, so one
// fix here covers both.
func TestBuildStatus_LongStatusMessageIsTruncatedNotWrapped(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 106
	m.reading = true // idle: no spinner/elapsed suffix competing for room
	// A long, realistic echo of a job's question — comfortably longer than
	// the 106-column frame on its own, before any padding/right side.
	m.statusMessage = strings.Repeat("this is a very long echoed job question ", 5)

	result := m.buildStatus()

	if got := lipgloss.Width(result); got > m.width {
		t.Fatalf("buildStatus() rendered width = %d, want <= m.width (%d); got %q", got, m.width, result)
	}
}

// TestBuildStatus_NewRefusalMessageFitsWidth pins the exact refusal string
// tui_keys.go's handleLocalSlashCommand sets on m.statusMessage for /new,
// /exit, /resume while a turn is in flight — the other overflow source cited
// in the fix above — through the same buildStatus path, at the narrow width
// where it was originally observed to overflow.
func TestBuildStatus_NewRefusalMessageFitsWidth(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 60
	m.reading = true
	m.statusMessage = "/new has to wait — it changes the conversation this turn is writing to. Esc stops the turn, then press Enter."

	result := m.buildStatus()

	if got := lipgloss.Width(result); got > m.width {
		t.Fatalf("buildStatus() rendered width = %d, want <= m.width (%d); got %q", got, m.width, result)
	}
}

// TestTruncateStatusText_CapsToMaxWidthWithEllipsis unit-tests the helper
// directly: it must never return a string wider than maxW, and a string
// that WAS truncated must end in "…" so the person can tell it was cut.
func TestTruncateStatusText_CapsToMaxWidthWithEllipsis(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := truncateStatusText(long, 20)

	if w := lipgloss.Width(got); w > 20 {
		t.Fatalf("truncateStatusText width = %d, want <= 20; got %q", w, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected truncated result to end in an ellipsis, got %q", got)
	}
}

// TestBuildStatus_NarrowTerminalNeverWraps is F1's regression test,
// hardened after review found the original single-width version (m.width =
// 50) passed even on the pre-fix code: that width happened to be wide
// enough for that one ledger to just barely fit. A single width cannot
// prove the invariant buildStatus actually needs — "never wider than
// m.width, for every m.width" — only a sweep can, and only a sweep catches
// the narrower failure bands review found (0..14, 21, 46, 48 on the first
// fix attempt: 1..14 because ctxPart itself was never width-bounded, 46/48
// because the budget left the right side exactly m.width wide with no
// margin for buildStatus's own 1-column left-side floor).
//
// Swept for three ledger shapes: main+subagent+scout (every breakdown
// clause active), main only (no breakdown at all, so ctxPart alone can
// still be the failure), and a session entirely on an unpriced model (the
// "$0.00" branch, which review found bypassed the width check completely).
// Width 0 is excluded from the sweep and treated separately below — see its
// own comment for why.
func TestBuildStatus_NarrowTerminalNeverWraps(t *testing.T) {
	cases := []struct {
		name    string
		catalog string
		record  func()
	}{
		{
			name: "main+subagent+scout",
			catalog: `{"p":{"id":"p","models":{
				"m":{"id":"m","name":"m","cost":{"input":3,"output":15},"limit":{"context":200000}}
			}}}`,
			record: func() {
				ledger.Record(ledger.Main, "p", "m", "", stream.Usage{Input: 198_000, Output: 100})
				ledger.Record(ledger.Subagent, "p", "m", "job-1", stream.Usage{Input: 1_000_000, Output: 1000})
				ledger.Record(ledger.Scout, "p", "m", "", stream.Usage{Input: 100_000, Output: 1000})
			},
		},
		{
			name: "main only",
			catalog: `{"p":{"id":"p","models":{
				"m":{"id":"m","name":"m","cost":{"input":3,"output":15},"limit":{"context":200000}}
			}}}`,
			record: func() {
				ledger.Record(ledger.Main, "p", "m", "", stream.Usage{Input: 198_000, Output: 100})
			},
		},
		{
			name: "fully unpriced session",
			catalog: `{"unpriced":{"id":"unpriced","models":{
				"m":{"id":"m","name":"m","limit":{"context":200000}}
			}}}`,
			record: func() {
				ledger.Record(ledger.Main, "unpriced", "m", "", stream.Usage{Input: 198_000, Output: 100})
				ledger.Record(ledger.Subagent, "unpriced", "m", "job-1", stream.Usage{Input: 1_000_000, Output: 1000})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTestCatalog(t, dir, tc.catalog)
			t.Setenv("HOME", dir)
			pricing.Reset()
			ledger.Reset()
			t.Cleanup(pricing.Reset)
			t.Cleanup(ledger.Reset)
			tc.record()

			m := newModel(nil, "p/m", "", []string{"p/m"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
			m.reading = true
			m.modelName = "m"
			m.lastUsage = stream.Usage{Input: 198_000, Output: 100}

			// Width 0 is buildContextCost's own documented "not yet
			// resized" state, rendered unbounded on purpose (see its
			// comment) — a live terminal never actually renders at width
			// 0, so it is not swept here, only smoke-tested for no panic.
			m.width = 0
			_ = m.buildStatus()

			for w := 1; w <= 120; w++ {
				m.width = w
				result := m.buildStatus()
				if got := lipgloss.Width(result); got > w {
					t.Fatalf("width=%d: buildStatus() rendered width = %d, want <= %d; got %q", w, got, w, result)
				}
			}
		})
	}
}

// TestAssembleStatusRow_OverlongRightPartIsClamped pins F34: the bar must
// clamp its RIGHT side itself, not rely on each right-side producer to
// bound its own output. buildContextCost (the only current producer)
// already caps itself, so an over-long right can never be observed via
// buildStatus today — the clamp lives in the shared assembly, exercised
// here with a right part far past the terminal width. Asserts one line
// (no wrap), no wider than the terminal.
func TestAssembleStatusRow_OverlongRightPartIsClamped(t *testing.T) {
	right := strings.Repeat("subsidy figures ", 10) // ~160 cols, no truncation hints

	for _, w := range []int{20, 40, 60, 106} {
		got := assembleStatusRow("⟳ thinking... 1.0s", right, w)
		if strings.Contains(got, "\n") {
			t.Fatalf("width=%d: assembleStatusRow wrapped to multiple lines: %q", w, got)
		}
		if gotW := lipgloss.Width(got); gotW > w {
			t.Fatalf("width=%d: assembleStatusRow width = %d, want <= %d; got %q", w, gotW, w, got)
		}
	}
}

// TestAssembleStatusRow_RightClampKeepsLeftAlive checks the clamp leaves
// room for the left side's 1-column floor rather than letting the right
// part eat the whole row: with an over-long right, the left part must
// still be present.
func TestAssembleStatusRow_RightClampKeepsLeftAlive(t *testing.T) {
	got := assembleStatusRow("model", strings.Repeat("x", 200), 40)
	if !strings.Contains(got, "model") && !strings.Contains(got, "…") {
		t.Fatalf("expected left part or its ellipsis in %q", got)
	}
	if w := lipgloss.Width(got); w > 40 {
		t.Fatalf("assembleStatusRow width = %d, want <= 40; got %q", w, got)
	}
}

// TestTruncateStatusText_ShortStringPassesThroughUnchanged makes sure the
// truncation is only applied when actually needed — no ellipsis, no
// mangling, for text that already fits.
func TestTruncateStatusText_ShortStringPassesThroughUnchanged(t *testing.T) {
	short := "fits fine"
	got := truncateStatusText(short, 50)
	if got != short {
		t.Fatalf("truncateStatusText(%q, 50) = %q, want it unchanged", short, got)
	}
}

// ─── #153: one width budget, and a right side that is never cut ─────────

// statusBarCostModel builds the session #153 was reported on: a model with a
// 200k window, a 198k context and a $0.596 bill, so buildContextCost renders
// "ctx 198k (99%), 0.596$" (22 columns) — the exact string the issue
// measured. The model name is the left side's only fragment ("m", 1 column),
// which keeps every assertion below about the right side unambiguous: the
// left is one character, so it can neither be ellipsized nor supply a
// fragment that a dropped item could be confused with.
func statusBarCostModel(t *testing.T, record func()) TuiModel {
	t.Helper()
	dir := t.TempDir()
	writeTestCatalog(t, dir, `{"p":{"id":"p","models":{
		"m":{"id":"m","name":"m","cost":{"input":3,"output":15},"limit":{"context":200000}}
	}}}`)
	t.Setenv("HOME", dir)
	pricing.Reset()
	ledger.Reset()
	t.Cleanup(pricing.Reset)
	t.Cleanup(ledger.Reset)
	record()

	m := newModel(nil, "p/m", "", []string{"p/m"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.reading = true // idle: no spinner/elapsed suffix competing for room
	m.modelName = "m"
	m.lastUsage = stream.Usage{Input: 198_000, Output: 100}
	return m
}

// TestBuildStatus_RightSideIsNeverCutMidFigure is #153's regression test. The
// status bar carried TWO width budgets for the same side — width-1 in
// buildContextCost and width-3 in assembleStatusRow — and the tighter one
// won, so every right side landing in (width-3, width-1] was ellipsized. The
// reported symptom was a $0.596 bill rendering as "0.59…" at 24 columns, in
// direct contradiction of formatCost's rule that a figure is omitted rather
// than cut, because a cut one misrepresents what the session has spent.
//
// Asserted over a sweep of every width, not the single width the bug was
// seen at (the sibling test above records why one width is not enough): the
// bar carries no ellipsis at all, a "$" on screen means the whole bill is
// there, and whatever buildContextCost decided to render reaches the row
// verbatim — that last one is the "one budget" invariant, and it is what
// failed at width 23 and 24.
func TestBuildStatus_RightSideIsNeverCutMidFigure(t *testing.T) {
	m := statusBarCostModel(t, func() {
		ledger.Record(ledger.Main, "p", "m", "", stream.Usage{Input: 198_000, Output: 100})
	})
	const wholeBill = "0.596$"

	for w := 1; w <= 120; w++ {
		m.width = w
		right := m.buildContextCost()
		bar := m.buildStatus()

		if strings.Contains(bar, "…") {
			t.Errorf("width=%d: status bar ellipsized a figure: %q", w, bar)
		}
		if strings.Contains(bar, "$") && !strings.Contains(bar, wholeBill) {
			t.Errorf("width=%d: status bar shows a partial bill, want %q whole or none of it: %q", w, wholeBill, bar)
		}
		if right != "" && !strings.Contains(bar, right) {
			t.Errorf("width=%d: assembled row cut the right side buildContextCost rendered (%q): %q", w, right, bar)
		}
		if got := lipgloss.Width(bar); got > w {
			t.Errorf("width=%d: status bar is %d columns wide: %q", w, got, bar)
		}
	}
}

// TestBuildStatus_RightSideMatchesShippedRenderingAtEveryCutWidth pins the
// widths where the reserve is decided, against what v0.1.0 rendered — this is
// the user's baseline, since both #125 and #153 are still in [Unreleased].
//
// The expected strings are the shipped ones, measured from the v0.1.0 tag
// (3860a4b), and deliberately not computed from statusRightReserve: the point
// of the table is to pin the *value* of the budget by what a person sees, so
// that growing the reserve by a column — which is safe for the wrap invariant
// and would still pass every other test in this file — fails here instead. It
// does: at 23 columns the bill is exactly 22 wide, so a reserve of 2 drops it
// and renders " m      ctx 198k (99%) " instead.
//
// Note what is NOT claimed: the left side still truncates from the tail, so a
// scroll offset renders as "↑1…" on a narrow bar (see truncateStatusText).
// This table is about the right side alone.
func TestBuildStatus_RightSideMatchesShippedRenderingAtEveryCutWidth(t *testing.T) {
	m := statusBarCostModel(t, func() {
		ledger.Record(ledger.Main, "p", "m", "", stream.Usage{Input: 198_000, Output: 100})
	})

	cases := []struct {
		width int
		want  string
	}{
		{7, "m0.596$"},                  // 6-wide right: fits next to a 1-column left
		{8, " m0.596$"},                 // ...and gets its leading space back
		{15, "mctx 198k (99%)"},         // the ctx figure swaps in for the bill
		{16, " mctx 198k (99%)"},        //
		{23, "mctx 198k (99%), 0.596$"}, // the reported case, one column tighter
		{24, " mctx 198k (99%), 0.596$"},
		{25, " mctx 198k (99%), 0.596$ "},
	}
	for _, c := range cases {
		m.width = c.width
		if got := m.buildStatus(); got != c.want {
			t.Errorf("buildStatus() at width %d = %q, want %q (as rendered by v0.1.0)", c.width, got, c.want)
		}
	}
}

// TestAssembleStatusRow_RightSideDropsWholeItems covers the assembler's own
// half of the rule, with a right side of several items so the drop order is
// observable: items leave from the end and whole, never one by one through
// truncateStatusText. Every item must be either present in full or absent
// without a fragment of it surviving (checked down to 2 columns, which is
// enough to catch any cut inside a figure like "0.596$").
//
// The left part is a single "m" — it can never be truncated, so an ellipsis
// anywhere in the row can only have come from the right side being cut.
// (The left side's own ellipsis-truncation is a separate, documented rule:
// there the useful content is at the start, not the end.)
func TestAssembleStatusRow_RightSideDropsWholeItems(t *testing.T) {
	items := []string{"ctx 198k (99%)", "0.596$", "12.34$ (sub 5.00$ scout 0.500$)"}
	right := strings.Join(items, statusSep)

	for w := 1; w <= 80; w++ {
		got := assembleStatusRow("m", right, w)
		if strings.Contains(got, "…") {
			t.Errorf("width=%d: right side was cut instead of dropped: %q", w, got)
		}
		if gotW := lipgloss.Width(got); gotW > w {
			t.Errorf("width=%d: assembleStatusRow width = %d; got %q", w, gotW, got)
		}
		for _, item := range items {
			if strings.Contains(got, item) {
				continue // kept whole, which is the only other legal outcome
			}
			runes := []rune(item)
			for n := 2; n < len(runes); n++ {
				if frag := string(runes[:n]); strings.Contains(got, frag) {
					t.Errorf("width=%d: fragment %q of dropped item %q survived: %q", w, frag, item, got)
				}
			}
		}
	}
}

// TestAssembleStatusRow_RightSideDropBoundaries pins the ladder's edges: the
// widths at which each item starts fitting, at which the last one stops
// fitting, and at which even the first one does not. The numbers are written
// out rather than computed from statusRightReserve, so the table pins what the
// budget buys (57 columns of right side at width 58) instead of restating the
// arithmetic that produced it.
func TestAssembleStatusRow_RightSideDropBoundaries(t *testing.T) {
	items := []string{"ctx 198k (99%)", "0.596$", "12.34$ (sub 5.00$ scout 0.500$)"} // 14 / 23 / 57 columns together
	right := strings.Join(items, statusSep)

	cases := []struct {
		width     int
		wantItems int
	}{
		{58, 3}, // 57 columns of right side: everything fits
		{57, 2}, // one column short: the last item goes
		{24, 2}, // the first two still fit
		{23, 1}, // 22 columns: only the ctx figure
		{15, 1}, // 14 columns: the ctx figure, just
		{14, 0}, // 13 columns: not even that
		{1, 0},  // ...down to the narrowest terminal there is
	}
	for _, c := range cases {
		got := assembleStatusRow("m", right, c.width)
		if want := strings.Join(items[:c.wantItems], statusSep); !strings.Contains(got, want) {
			t.Errorf("width=%d: want the first %d item(s) %q kept whole; got %q", c.width, c.wantItems, want, got)
		}
		for _, item := range items[c.wantItems:] {
			if strings.Contains(got, item) {
				t.Errorf("width=%d: item %q is over the budget but survived: %q", c.width, item, got)
			}
		}
		if gotW := lipgloss.Width(got); gotW > c.width {
			t.Errorf("width=%d: assembleStatusRow width = %d; got %q", c.width, gotW, got)
		}
		if strings.Contains(got, "…") {
			t.Errorf("width=%d: an item was cut instead of dropped: %q", c.width, got)
		}
	}
}

// TestFitStatusRight_EmptyItemDoesNotLeaveATrailingSeparator guards the one
// input shape that could make a drop manufacture a separator: buildStatus
// never appends an empty item, but a future producer might, and then
// "a │  │ b" would keep "a │ " — a separator with nothing after it. Both the
// empty item and the whitespace-only one are checked, since the second is
// what a doubled separator actually produces once split.
func TestFitStatusRight_EmptyItemDoesNotLeaveATrailingSeparator(t *testing.T) {
	for _, input := range []string{
		"a" + statusSep + statusSep + "b",
		"a" + statusSep + " " + statusSep + "b",
	} {
		got := fitStatusRight(input, 6)
		if got != "a"+statusSep+"b" {
			t.Errorf("fitStatusRight(%q, 6) = %q, want the empty item skipped: %q", input, got, "a"+statusSep+"b")
		}
		if strings.HasPrefix(got, statusSep) || strings.HasSuffix(got, statusSep) {
			t.Errorf("fitStatusRight(%q, 6) = %q, a separator with no item on one side of it", input, got)
		}
	}
}

// TestBuildContextCost_HonorsTheSharedRightBudget is the producer's half of
// "one budget in one place": whatever buildContextCost renders must fit
// statusRightBudget, the number assembleStatusRow enforces. A producer over
// budget is the failure #153 was — the bar dropped its tail one layer above,
// silently — so this asserts against the shared helper rather than a literal.
// Two honest limits: a producer that does not CALL the helper cannot be caught
// here at the tight bound (a local m.width-1 and statusRightBudget are the same
// number), and what it does catch is a producer that budgets wider than the
// bar, at any width. The helper's VALUE is pinned elsewhere, by the shipped
// rendering in TestBuildStatus_RightSideMatchesShippedRenderingAtEveryCutWidth,
// which a wrong constant cannot pass.
//
// Swept over every width on the widest ledger shape (a full breakdown, not the
// bare total) so the whole ladder in formatCost is exercised at a narrow
// width, plus a budget-free check that a rendered cost is always the complete
// figure from the ledger rather than a prefix of it.
func TestBuildContextCost_HonorsTheSharedRightBudget(t *testing.T) {
	m := statusBarCostModel(t, func() {
		ledger.Record(ledger.Main, "p", "m", "", stream.Usage{Input: 198_000, Output: 100})
		ledger.Record(ledger.Subagent, "p", "m", "job-1", stream.Usage{Input: 1_000_000, Output: 1000})
		ledger.Record(ledger.Scout, "p", "m", "", stream.Usage{Input: 100_000, Output: 1000})
	})
	bareTotal := fmtUSD(ledger.Get().TotalUSD()) + "$"

	for w := 1; w <= 120; w++ {
		m.width = w
		got := m.buildContextCost()
		if strings.Contains(got, "…") {
			t.Errorf("width=%d: buildContextCost ellipsized a figure: %q", w, got)
		}
		if strings.Contains(got, "$") && !strings.Contains(got, bareTotal) {
			t.Errorf("width=%d: buildContextCost rendered a partial bill, want %q whole or none of it: %q", w, bareTotal, got)
		}
		if gotW := lipgloss.Width(got); gotW > statusRightBudget(w) {
			t.Errorf("width=%d: buildContextCost is %d columns wide, want <= %d (statusRightBudget): %q",
				w, gotW, statusRightBudget(w), got)
		}
	}
}

// TestStatusBarRendersWholeFiguresBeforeTheFirstResize pins the one width rule
// that is not about columns: m.width <= 0 means "no resize yet", so both sides
// render whole and the row may be wider than the (still unknown) terminal.
// The frame is never painted — paintRegion returns early on width <= 0 — so
// this contract exists only so the pre-resize frame does not come out as a
// stub with its figures silently dropped. A budget that floored at 0 here
// would look fine in every width sweep and still blank the bar on startup.
func TestStatusBarRendersWholeFiguresBeforeTheFirstResize(t *testing.T) {
	m := statusBarCostModel(t, func() {
		ledger.Record(ledger.Main, "p", "m", "", stream.Usage{Input: 198_000, Output: 100})
	})
	m.width = 0

	const whole = "ctx 198k (99%), 0.596$"
	if got := m.buildContextCost(); got != whole {
		t.Fatalf("buildContextCost() before the first resize = %q, want %q (unbounded, not dropped)", got, whole)
	}
	if got := m.buildStatus(); !strings.Contains(got, whole) {
		t.Fatalf("buildStatus() before the first resize dropped a figure: %q", got)
	}
	if got := assembleStatusRow("model", whole, 0); !strings.Contains(got, whole) {
		t.Fatalf("assembleStatusRow at width 0 cut the right side: %q", got)
	}
}
