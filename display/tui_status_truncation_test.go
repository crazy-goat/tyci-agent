package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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
