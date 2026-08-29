package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/internal/pricing"
	"github.com/decodo/tyci/stream"
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
