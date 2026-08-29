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

// TestBuildStatus_ScoutCostOnNarrowTerminalDoesNotWrap is F1's regression
// test: item 21 round B added a second parenthetical (scout) to the right
// side of the status bar, which buildStatus's own truncation (above) never
// covers — only the LEFT side is capped against the right's width, the
// right side itself never is. On a narrow terminal with main + subagent +
// scout spend, the pre-fix right side ("ctx ..., X.XX$ (sub Y$) (scout Z$)")
// could exceed m.width on its own, wrapping the whole fixed-height status
// row exactly like the overlong-message failure this file's other tests
// guard against.
func TestBuildStatus_ScoutCostOnNarrowTerminalDoesNotWrap(t *testing.T) {
	dir := t.TempDir()
	writeTestCatalog(t, dir, `{"p":{"id":"p","models":{
		"m":{"id":"m","name":"m","cost":{"input":3,"output":15},"limit":{"context":200000}}
	}}}`)
	t.Setenv("HOME", dir)
	pricing.Reset()
	ledger.Reset()
	t.Cleanup(pricing.Reset)
	t.Cleanup(ledger.Reset)

	ledger.Record(ledger.Main, "p", "m", "", stream.Usage{Input: 198_000, Output: 100})
	ledger.Record(ledger.Subagent, "p", "m", "job-1", stream.Usage{Input: 1_000_000, Output: 1000})
	ledger.Record(ledger.Scout, "p", "m", "", stream.Usage{Input: 100_000, Output: 1000})

	m := newModel(nil, "p/m", "", []string{"p/m"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 50
	m.reading = true
	m.modelName = "m"
	m.lastUsage = stream.Usage{Input: 198_000, Output: 100}

	result := m.buildStatus()

	if got := lipgloss.Width(result); got > m.width {
		t.Fatalf("buildStatus() rendered width = %d, want <= m.width (%d); got %q", got, m.width, result)
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
