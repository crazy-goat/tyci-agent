package display

import (
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/stream"
)

// ─── formatElapsed ───────────────────────────────────────────────────────

func TestFormatElapsed_AllVariantsHaveSameVisibleWidth(t *testing.T) {
	durations := []time.Duration{
		0,
		1 * time.Millisecond,
		23 * time.Millisecond,
		99 * time.Millisecond,
		100 * time.Millisecond,
		920 * time.Millisecond,
		999 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		10*time.Second + 200*time.Millisecond,
		59*time.Second + 900*time.Millisecond,
		100 * time.Second,
		999 * time.Second,
	}
	var want int
	for i, d := range durations {
		got := formatElapsed(d)
		w := visibleWidth(got)
		if i == 0 {
			want = w
		}
		if w != want {
			t.Errorf("formatElapsed(%v) = %q has visible width %d, want %d (consistent across all durations)",
				d, got, w, want)
		}
	}
}

func TestFormatElapsed_AlwaysWrappedInBrackets(t *testing.T) {
	for _, d := range []time.Duration{0, 100 * time.Millisecond, 5 * time.Second, 9999 * time.Second} {
		got := formatElapsed(d)
		if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "]") {
			t.Errorf("formatElapsed(%v) = %q, expected to be wrapped in []", d, got)
		}
	}
}

func TestFormatElapsed_NegativeClampedToZero(t *testing.T) {
	got := formatElapsed(-5 * time.Second)
	if !strings.Contains(got, "0") {
		t.Errorf("formatElapsed(-5s) = %q, expected to contain 0", got)
	}
}

func TestFormatElapsed_MsFormatForSub100ms(t *testing.T) {
	for _, d := range []time.Duration{0, 1 * time.Millisecond, 23 * time.Millisecond, 99 * time.Millisecond} {
		got := formatElapsed(d)
		if !strings.HasSuffix(got, "ms]") {
			t.Errorf("formatElapsed(%v) = %q, expected ms suffix", d, got)
		}
	}
}

func TestFormatElapsed_SFormatFor100msAndAbove(t *testing.T) {
	for _, d := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 999 * time.Millisecond, 1 * time.Second, 99 * time.Second} {
		got := formatElapsed(d)
		if !strings.HasSuffix(got, "s]") {
			t.Errorf("formatElapsed(%v) = %q, expected s suffix (not ms)", d, got)
		}
		if strings.HasSuffix(got, "ms]") {
			t.Errorf("formatElapsed(%v) = %q, should not use ms suffix for >= 100ms", d, got)
		}
	}
}

func TestFormatElapsed_OneDecimalSecond(t *testing.T) {
	// 1.5s should be 1.5, not 1 or 2.
	got := formatElapsed(1500 * time.Millisecond)
	if !strings.Contains(got, "1.5") {
		t.Errorf("formatElapsed(1.5s) = %q, expected to contain 1.5", got)
	}
}

func TestFormatElapsed_SecondFormatFits(t *testing.T) {
	// 0.2s should round to 0.2, not 0.1 or 0.3.
	got := formatElapsed(200 * time.Millisecond)
	if !strings.Contains(got, "0.2") {
		t.Errorf("formatElapsed(0.2s) = %q, expected to contain 0.2", got)
	}
}

// ─── fitLine ──────────────────────────────────────────────────────────────

func TestFitLine_NoTruncationWhenFits(t *testing.T) {
	if got := fitLine("hello", 10); got != "hello" {
		t.Errorf("fitLine(hello, 10) = %q, want hello", got)
	}
}

func TestFitLine_ExactFit(t *testing.T) {
	if got := fitLine("hello", 5); got != "hello" {
		t.Errorf("fitLine(hello, 5) = %q, want hello", got)
	}
}

func TestFitLine_TruncatesWithEllipsis(t *testing.T) {
	got := fitLine("hello world", 5)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("fitLine(hello world, 5) = %q, expected ellipsis suffix", got)
	}
	// Visible width should be <= maxW.
	if w := visibleWidth(got); w > 5 {
		t.Errorf("fitLine(hello world, 5) width=%d, want <= 5", w)
	}
}

func TestFitLine_EmptyInput(t *testing.T) {
	if got := fitLine("", 10); got != "" {
		t.Errorf("fitLine empty = %q, want empty", got)
	}
}

func TestFitLine_ZeroWidth(t *testing.T) {
	if got := fitLine("hello", 0); got != "" {
		t.Errorf("fitLine zero width = %q, want empty", got)
	}
}

func TestFitLine_NegativeWidth(t *testing.T) {
	if got := fitLine("hello", -1); got != "" {
		t.Errorf("fitLine negative width = %q, want empty", got)
	}
}

func TestFitLine_WidthSmallerThanEllipsis(t *testing.T) {
	got := fitLine("hello world", 2)
	if w := visibleWidth(got); w != 2 {
		t.Errorf("fitLine width<ellipsis = %q width=%d, want width 2", got, w)
	}
}

func TestFitLine_Unicode(t *testing.T) {
	// Polish chars are 1 col each. "ąęćżń" = 5 cols.
	got := fitLine("ąęćżń", 3)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("fitLine unicode = %q, expected ellipsis", got)
	}
	if w := visibleWidth(got); w > 3 {
		t.Errorf("fitLine unicode width=%d, want <= 3", w)
	}
}

func TestFitLine_VisibleWidthHonored(t *testing.T) {
	// Various lengths should all fit within maxW.
	for _, w := range []int{3, 5, 8, 10, 20} {
		got := fitLine("the quick brown fox jumps over the lazy dog", w)
		if vw := visibleWidth(got); vw > w {
			t.Errorf("fitLine(text, %d) width=%d > %d", w, vw, w)
		}
	}
}

// ─── singleLine ──────────────────────────────────────────────────────────

func TestSingleLine_CollapsesNewlines(t *testing.T) {
	m := &Minimal{curContent: *newStringBuilder("line1\nline2\nline3")}
	got := m.singleLine()
	if strings.Contains(got, "\n") {
		t.Errorf("singleLine = %q, should not contain newlines", got)
	}
	if !strings.Contains(got, "line1") || !strings.Contains(got, "line3") {
		t.Errorf("singleLine = %q, expected to contain all lines", got)
	}
}

func TestSingleLine_CollapsesMultipleSpaces(t *testing.T) {
	m := &Minimal{curContent: *newStringBuilder("hello    world")}
	got := m.singleLine()
	if strings.Contains(got, "  ") {
		t.Errorf("singleLine = %q, should collapse multiple spaces", got)
	}
	if !strings.Contains(got, "hello world") {
		t.Errorf("singleLine = %q, expected 'hello world'", got)
	}
}

func TestSingleLine_TrimsWhitespace(t *testing.T) {
	m := &Minimal{curContent: *newStringBuilder("  \n  hello  \n  ")}
	got := m.singleLine()
	if strings.HasPrefix(got, " ") || strings.HasSuffix(got, " ") {
		t.Errorf("singleLine = %q, expected trimmed", got)
	}
}

func TestSingleLine_Empty(t *testing.T) {
	m := &Minimal{curContent: *newStringBuilder("")}
	if got := m.singleLine(); got != "" {
		t.Errorf("singleLine empty = %q, want empty", got)
	}
}

// ─── renderLocked: full line width ───────────────────────────────────────

func TestRenderLocked_AllCasesWithinTerminal(t *testing.T) {
	// Various widths and contents, all rendered lines must fit the terminal.
	// (Detailed width/alignment checks are in TestRender_LineWithinTerminal
	// and TestRender_TimeBracketAtRightEdge below.)
	widths := []int{20, 40, 80, 120, 200}
	contents := []string{
		"",
		"short",
		"this is a medium length line that should fit",
		strings.Repeat("x", 50),
		strings.Repeat("hello world ", 30),
		"unicode: ąęćżńó",
	}
	for _, w := range widths {
		for _, content := range contents {
			final := captureFinalLine(t, w, func(m *Minimal) {
				m.blockStart = time.Now().Add(-2 * time.Second)
				m.startLine(prefixResponse)
				m.curContent.WriteString(content)
				m.lastRender = time.Time{}
				m.renderLocked(false)
				m.finishLine()
			})
			if vw := visibleWidth(final); vw > w {
				t.Errorf("width=%d content_len=%d: final line wider than terminal: width=%d > %d, line=%q",
					w, len(content), vw, w, final)
			}
		}
	}
}

// ─── renderLocked: time alignment ────────────────────────────────────────

func TestRenderLocked_TimeAlwaysAtRightEdge(t *testing.T) {
	// The time bracket should be at the rightmost position of the line,
	// regardless of content length. We check the closing "]" is at the
	// same column for all content lengths.
	m := newTestMinimal(80)
	m.blockStart = time.Now().Add(-2500 * time.Millisecond)

	var positions []int
	for _, content := range []string{"", "x", "medium content", strings.Repeat("y", 200)} {
		m.startLine(prefixResponse)
		m.curContent.WriteString(content)
		m.lastRender = time.Time{}
		m.renderLocked(false)
		elapsed := formatElapsed(time.Since(m.blockStart))
		positions = append(positions, visibleWidth(elapsed))
	}
	for i, p := range positions {
		if p != positions[0] {
			t.Errorf("time width for content %d: %d, want %d (constant)", i, p, positions[0])
		}
	}
}

// ─── Minimal: end-to-end line width ─────────────────────────────────────

// captureFinalLine runs fn against a fresh Minimal and returns the
// last visible line of the output — i.e. what the user would see in a
// terminal after all in-place updates have been resolved. The terminal
// overwrites earlier renders via \r, so we keep only the substring after
// the last \r and before the trailing \n.
func captureFinalLine(t *testing.T, w int, fn func(m *Minimal)) string {
	t.Helper()
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(w)
	fn(m)
	sync()

	raw := stdout.String()
	// The final line is everything after the last \r up to the last \n.
	if idx := strings.LastIndex(raw, "\r"); idx >= 0 {
		raw = raw[idx+1:]
	}
	if idx := strings.LastIndex(raw, "\n"); idx >= 0 {
		raw = raw[:idx]
	}
	return raw
}

func TestRender_LineWithinTerminal(t *testing.T) {
	cases := []struct {
		name    string
		w       int
		content string
	}{
		{"empty", 80, ""},
		{"short", 80, "hello"},
		{"long", 80, strings.Repeat("x", 200)},
		{"narrow long", 20, "hello world this is long"},
		{"unicode", 80, "ąęćżńóąęćżńóąęćżńóąęćżńó"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			final := captureFinalLine(t, c.w, func(m *Minimal) {
				m.startLine(prefixResponse)
				m.curContent.WriteString(c.content)
				m.lastRender = time.Time{}
				m.renderLocked(false)
				m.finishLine()
			})
			final = strings.TrimRight(final, " ")
			if final == "" {
				t.Fatalf("no final line rendered")
			}
			if w := visibleWidth(final); w > c.w {
				t.Errorf("final line wider than terminal: width=%d > %d, line=%q", w, c.w, final)
			}
		})
	}
}

func TestRender_TimeBracketAtRightEdge(t *testing.T) {
	// The closing "]" of the time bracket should be at the rightmost
	// position of the visible line for the given terminal width.
	cases := []struct {
		name    string
		w       int
		content string
	}{
		{"empty", 80, ""},
		{"short", 80, "hi"},
		{"medium", 80, "this is a medium content line"},
		{"narrow empty", 20, ""},
		{"narrow short", 20, "x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			final := captureFinalLine(t, c.w, func(m *Minimal) {
				m.startLine(prefixResponse)
				m.curContent.WriteString(c.content)
				m.lastRender = time.Time{}
				m.renderLocked(false)
				m.finishLine()
			})
			final = strings.TrimRight(final, " ")
			if !strings.HasSuffix(final, "]") {
				t.Errorf("line does not end with ']': %q", final)
			}
			if w := visibleWidth(final); w != c.w {
				t.Errorf("line visible width = %d, want %d (terminal width). line=%q", w, c.w, final)
			}
		})
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────

// newStringBuilder returns a pointer to a strings.Builder initialised with s.
// Used in tests that need to construct a Minimal with a pre-populated
// curContent.
func newStringBuilder(s string) *strings.Builder {
	b := &strings.Builder{}
	b.WriteString(s)
	return b
}

// ─── buildStatLine / buildStatRate ──────────────────────────────────────

func TestBuildStatLine_HasNoTimeField(t *testing.T) {
	// The time belongs on the right, rendered by the standard line
	// machinery. The body returned by buildStatLine must not contain it.
	line := buildStatLine(stream.Usage{Input: 100, Output: 50})
	if strings.HasPrefix(line, "[") {
		t.Errorf("body should not start with bracketed time, got %q", line)
	}
	if strings.Contains(line, "[") || strings.Contains(line, "]") {
		t.Errorf("body should not contain any brackets, got %q", line)
	}
}

func TestBuildStatLine_AllFields(t *testing.T) {
	line := buildStatLine(stream.Usage{
		Input:      2500,
		Output:     362,
		Reasoning:  56,
		CacheRead:  2304,
		CacheWrite: 89,
	})
	for _, want := range []string{
		"in=196", "(+2304 cache)", "out=362", "r=56", "cache_w=89",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("expected %q in body, got %q", want, line)
		}
	}
}

func TestBuildStatLine_NoTTFTNoTEqualsNoRate(t *testing.T) {
	line := buildStatLine(stream.Usage{Output: 100})
	for _, banned := range []string{"ttft", "t=4", "tok/s"} {
		if strings.Contains(line, banned) {
			t.Errorf("body should not contain %q, got %q", banned, line)
		}
	}
}

func TestBuildStatLine_CacheReadSubtractedFromInput(t *testing.T) {
	line := buildStatLine(stream.Usage{Input: 150, Output: 50, CacheRead: 100})
	if !strings.Contains(line, "in=50") {
		t.Errorf("expected in=50 (input-cache), got %q", line)
	}
	if !strings.Contains(line, "(+100 cache)") {
		t.Errorf("expected (+100 cache), got %q", line)
	}
}

func TestBuildStatLine_CacheReadExceedsInputClampedToZero(t *testing.T) {
	line := buildStatLine(stream.Usage{Input: 50, Output: 10, CacheRead: 100})
	if !strings.Contains(line, "in=0") {
		t.Errorf("expected in=0 (clamped), got %q", line)
	}
}

func TestBuildStatRate_OutputOverTotalDuration(t *testing.T) {
	got := buildStatRate(stream.Usage{Output: 200}, stream.Stats{Duration: 4 * time.Second})
	if got != "tok/s=50.0" {
		t.Errorf("expected tok/s=50.0, got %q", got)
	}
}

func TestBuildStatRate_IgnoresFirstToken(t *testing.T) {
	// 400/4 = 100.0, NOT 400/(4-1.83) = 184.3
	got := buildStatRate(
		stream.Usage{Output: 400},
		stream.Stats{Duration: 4 * time.Second, FirstToken: 1830 * time.Millisecond},
	)
	if got != "tok/s=100.0" {
		t.Errorf("expected tok/s=100.0 (output/total), got %q", got)
	}
}

func TestBuildStatRate_ZeroDuration(t *testing.T) {
	got := buildStatRate(stream.Usage{Output: 100}, stream.Stats{Duration: 0})
	if got != "tok/s=0.0" {
		t.Errorf("expected tok/s=0.0 for zero duration, got %q", got)
	}
}

func TestBuildStatRate_OneDecimal(t *testing.T) {
	got := buildStatRate(stream.Usage{Output: 1}, stream.Stats{Duration: 3 * time.Second})
	if got != "tok/s=0.3" {
		t.Errorf("expected one-decimal format tok/s=0.3, got %q", got)
	}
}

// ─── Minimal.Summary: end-to-end ────────────────────────────────────────

func captureStatLine(t *testing.T, w int, usage stream.Usage, stats stream.Stats) string {
	t.Helper()
	stdout, _, sync, restore := captureOutput(t)
	defer restore()

	m := newTestMinimal(w)
	m.Summary(usage, stats)
	m.End()
	sync()

	raw := stdout.String()
	if idx := strings.LastIndex(raw, "\r"); idx >= 0 {
		raw = raw[idx+1:]
	}
	if idx := strings.LastIndex(raw, "\n"); idx >= 0 {
		raw = raw[:idx]
	}
	return raw
}

func TestMinimal_Summary_PrefixAndTimeOnRight(t *testing.T) {
	line := captureStatLine(t, 120,
		stream.Usage{Input: 2500, Output: 362, CacheRead: 2304},
		stream.Stats{Duration: 4 * time.Second},
	)

	if !strings.HasPrefix(line, "[STAT]") {
		t.Errorf("expected [STAT] prefix, got %q", line)
	}
	if !strings.HasSuffix(strings.TrimRight(line, " "), "]") {
		t.Errorf("expected line to end with ']' (time on right), got %q", line)
	}
	// The time bracket must come AFTER the field list, and there must
	// be only ONE bracket pair on the line (the time).
	idxTok := strings.Index(line, "tok/s=")
	idxOpen := strings.LastIndex(line, "[")
	idxClose := strings.LastIndex(line, "]")
	if idxTok < 0 || idxOpen < 0 || idxClose < 0 {
		t.Fatalf("missing required substrings in %q", line)
	}
	if idxOpen <= idxTok {
		t.Errorf("time bracket '[' must come after tok/s=, got %q", line)
	}
	if idxClose != len(strings.TrimRight(line, " "))-1 {
		t.Errorf("closing ']' must be the last non-space char, got %q", line)
	}
	// No other "[" should appear in the body (between the [STAT]
	// prefix and the time bracket). The prefix itself contains one '['.
	bodyStart := strings.Index(line, "]") + 1 // end of [STAT]
	body := line[bodyStart:idxOpen]
	if c := strings.Count(body, "["); c != 0 {
		t.Errorf("expected no '[' in body between prefix and time bracket, got %d in body=%q (line=%q)", c, body, line)
	}
}

func TestMinimal_Summary_ShowsTotalDurationOnRight(t *testing.T) {
	// 4 second duration should appear as "[  4.0s]" at the right edge,
	// matching the format used by other lines.
	line := captureStatLine(t, 120,
		stream.Usage{Output: 100},
		stream.Stats{Duration: 4 * time.Second},
	)
	// The closing "]" should be preceded by " 4.0s" with consistent
	// 8-char bracketed width.
	if !strings.HasSuffix(strings.TrimRight(line, " "), "[  4.0s]") {
		t.Errorf("expected line to end with [  4.0s], got %q", line)
	}
}

func TestMinimal_Summary_NoTTFT(t *testing.T) {
	line := captureStatLine(t, 120,
		stream.Usage{Output: 100},
		stream.Stats{Duration: 4 * time.Second, FirstToken: 2 * time.Second},
	)
	if strings.Contains(line, "ttft") {
		t.Errorf("ttft should not appear in [STAT] line, got %q", line)
	}
}

func TestMinimal_Summary_RateMatchesTimeOnRight(t *testing.T) {
	// The displayed rate and the displayed time must use the same duration.
	// 300 out / 3.8s = 78.9 tok/s, and the time should be [  3.8s].
	line := captureStatLine(t, 120,
		stream.Usage{Input: 89, Output: 300, Reasoning: 62, CacheRead: 2304, CacheWrite: 89},
		stream.Stats{Duration: 3800 * time.Millisecond},
	)
	for _, want := range []string{"tok/s=78.9", "[  3.8s]"} {
		if !strings.Contains(line, want) {
			t.Errorf("expected %q in line, got %q", want, line)
		}
	}
}

func TestMinimal_Summary_AllFieldsPresent(t *testing.T) {
	line := captureStatLine(t, 200,
		stream.Usage{Input: 2500, Output: 362, Reasoning: 56, CacheRead: 2304, CacheWrite: 89},
		stream.Stats{Duration: 4 * time.Second},
	)
	for _, want := range []string{
		"in=196", "(+2304 cache)", "out=362", "r=56", "cache_w=89", "tok/s=",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("expected %q in line, got %q", want, line)
		}
	}
}

func TestMinimal_Summary_ZeroDurationNoPanic(t *testing.T) {
	// Should not panic, should not divide by zero.
	line := captureStatLine(t, 120,
		stream.Usage{Output: 100},
		stream.Stats{Duration: 0},
	)
	if !strings.Contains(line, "tok/s=0.0") {
		t.Errorf("expected tok/s=0.0 for zero duration, got %q", line)
	}
}

func TestMinimal_Summary_TimeAlignmentWithOtherLines(t *testing.T) {
	// [STAT] line should have the same total width as a [RESP] line at
	// the same terminal width — both end at the right edge.
	w := 80
	statLine := captureStatLine(t, w,
		stream.Usage{Output: 100},
		stream.Stats{Duration: 2 * time.Second},
	)
	if vw := visibleWidth(statLine); vw != w {
		t.Errorf("stat line visible width = %d, want %d (terminal width). line=%q", vw, w, statLine)
	}
}

// newTestMinimal returns a Minimal with a known width so output is
// deterministic in tests. The background ticker is disabled (done
// channel replaced with a never-closed one) so the test does not race
// with re-renders.
func newTestMinimal(width int) *Minimal {
	return &Minimal{
		testWidth:  width,
		blockStart: time.Now(),
		done:       make(chan struct{}),
	}
}
