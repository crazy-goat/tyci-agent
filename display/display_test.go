package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/rivo/uniseg"
)

// --- Tests for stripAnsi ---

func TestStripAnsi_EmptyString(t *testing.T) {
	if got := stripAnsi(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestStripAnsi_NoAnsi(t *testing.T) {
	input := "hello world"
	if got := stripAnsi(input); got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestStripAnsi_WithAnsi(t *testing.T) {
	input := "\033[31mred\033[0m"
	expected := "red"
	if got := stripAnsi(input); got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestStripAnsi_MultipleAnsi(t *testing.T) {
	input := "\033[1m\033[31mbold red\033[0m"
	expected := "bold red"
	if got := stripAnsi(input); got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestStripAnsi_ClearLine(t *testing.T) {
	input := "a\033[Kb"
	expected := "ab"
	if got := stripAnsi(input); got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

// --- Tests for visibleWidth ---

func TestVisibleWidth_Empty(t *testing.T) {
	if got := visibleWidth(""); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestVisibleWidth_Plain(t *testing.T) {
	if got := visibleWidth("hello"); got != 5 {
		t.Errorf("expected 5, got %d", got)
	}
}

func TestVisibleWidth_WithAnsi(t *testing.T) {
	input := "\033[31mhello\033[0m"
	if got := visibleWidth(input); got != 5 {
		t.Errorf("expected 5, got %d", got)
	}
}

func TestVisibleWidth_Unicode(t *testing.T) {
	input := "💭 hello"
	if got := visibleWidth(input); got != 8 { // 💭 is 2 columns, space=1, hello=5
		t.Errorf("expected 8, got %d", got)
	}
}

// --- Tests for wrapText ---

func TestWrapText_ZeroWidth(t *testing.T) {
	input := "hello"
	if got := wrapText(input, 0, 0); got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestWrapText_NegativeWidth(t *testing.T) {
	input := "hello"
	if got := wrapText(input, -1, 0); got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestWrapText_EmptyString(t *testing.T) {
	if got := wrapText("", 10, 0); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestWrapText_ShortLine(t *testing.T) {
	input := "hello"
	got := wrapText(input, 80, 0)
	if got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestWrapText_ExactFit(t *testing.T) {
	input := "hello"
	got := wrapText(input, 5, 0)
	if got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestWrapText_LongLine(t *testing.T) {
	input := "1234567890abcdef"
	got := wrapText(input, 10, 0)
	// Should wrap at 10 chars
	expected := "1234567890\033[K\nabcdef"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_MultipleWraps(t *testing.T) {
	input := "123456789012345678901234567890"
	got := wrapText(input, 10, 0)
	expected := "1234567890\033[K\n1234567890\033[K\n1234567890"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_MultipleLines(t *testing.T) {
	input := "short\n1234567890abcdef"
	got := wrapText(input, 10, 0)
	expected := "short\n1234567890\033[K\nabcdef"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_WithAnsi(t *testing.T) {
	// ANSI sequences should not count towards width
	input := "\033[31m1234567890\033[0mabcdef"
	got := wrapText(input, 10, 0)
	// The visible part is "1234567890abcdef" = 16 chars
	// First 10 visible chars: "1234567890" (with ANSI around)
	// Then "\033[K\n"
	// Then "abcdef" (with ANSI reset before it? No, the ANSI reset is at end)
	// Actually the input is: \033[31m1234567890\033[0mabcdef
	// After wrapping at visual position 10:
	// Part1: \033[31m1234567890\033[0m (10 visible chars)
	// Part2: abcdef
	// Expected: \033[31m1234567890\033[0m\033[K\nabcdef
	expected := "\033[31m1234567890\033[0m\033[K\nabcdef"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_WithAnsiSpanningWrap(t *testing.T) {
	// ANSI at start, then text that wraps
	input := "\033[31m1234567890abcdef\033[0m"
	got := wrapText(input, 10, 0)
	// First 10 visible: 1234567890, all red
	// Then clearLine + newline
	// Then remaining: abcdef, still red (no reset until end)
	expected := "\033[31m1234567890\033[K\nabcdef\033[0m"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_WithUnicode(t *testing.T) {
	input := "💭1234567890abcdef"
	got := wrapText(input, 10, 0)
	// 💭 is one grapheme cluster that is two cells wide, so 10
	// columns hold "💭12345678". The wrap must not split the
	// cluster, whatever its width.
	expected := "💭12345678\033[K\n90abcdef"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

// startsMidCluster reports whether line begins inside a grapheme
// cluster: a continuation rune (combining mark, ZWJ, skin-tone
// modifier, ...) joins the prepended "x" into one cluster with the
// line's first rune, so the first cluster is longer than "x".
func startsMidCluster(line string) bool {
	if line == "" {
		return false
	}
	cluster, _, _, _ := uniseg.FirstGraphemeClusterInString("x"+line, -1)
	return cluster != "x"
}

// TestWrapText_GraphemeClusters checks that wrapping treats a
// grapheme cluster as one token: it is never split across lines
// and never wrapped past the width. Splitting a cluster leaves a
// continuation rune at the start of the next line, which the
// terminal draws in its own cell while every width measure counts
// it as zero — the row then draws wider than the TUI measured, and
// the sidebar border moves on that row (#579).
func TestWrapText_GraphemeClusters(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
	}{
		{"devanagari split mid conjunct", "abधन्यवाद", 4},
		{"devanagari word", "धन्यवाद", 3},
		{"devanagari conjunct", "क्षत्रिय", 3},
		{"combining accent orphaned", "cafe\u0301", 4},
		{"skin tone emoji overflow", "a👍🏽b", 2},
		{"zwj family emoji", "x👨‍👩‍👧‍👦y", 2},
		{"ascii unchanged", "1234567890abcdef", 10},
	}
	for _, tt := range tests {
		got := wrapText(tt.input, tt.width, 0)
		var joined strings.Builder
		for i, line := range strings.Split(got, "\n") {
			line = strings.TrimSuffix(line, clearLine)
			joined.WriteString(line)
			if line == "" {
				continue
			}
			if w := lipgloss.Width(line); w > tt.width {
				t.Errorf("%s: line %d %q is %d columns wide, want at most %d", tt.name, i, line, w, tt.width)
			}
			if startsMidCluster(line) {
				t.Errorf("%s: line %d %q starts inside a grapheme cluster", tt.name, i, line)
			}
		}
		if joined.String() != tt.input {
			t.Errorf("%s: wrapping lost or reordered content: got %q, want %q", tt.name, joined.String(), tt.input)
		}
	}
}

// TestWrapRawText_GraphemeClusters pins the main-column wrapping
// of combining text: user blocks and streaming tails render through
// wrapRawText, and every rendered line must fit the column without
// splitting a cluster (#579).
func TestWrapRawText_GraphemeClusters(t *testing.T) {
	content := strings.Repeat("धन्यवाद", 8) + " cafe\u0301 👍🏽 👨‍👩‍👧‍👦"
	for i, line := range strings.Split(wrapRawText(content, false, 40), "\n") {
		line = strings.TrimSuffix(line, clearLine)
		if w := lipgloss.Width(line); w > 40 {
			t.Errorf("line %d %q is %d columns wide, want at most 40", i, line, w)
		}
		if startsMidCluster(line) {
			t.Errorf("line %d %q starts inside a grapheme cluster", i, line)
		}
	}
}

func TestWrapText_AlreadyHasClearLine(t *testing.T) {
	input := "1234567890\033[K\nabcdef"
	got := wrapText(input, 10, 0)
	// The first line is exactly 10 visible + clearLine, so no wrap needed
	expected := "1234567890\033[K\nabcdef"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestWrapText_LongLineWithClearLine(t *testing.T) {
	input := "1234567890abcdef\033[K\nxyz"
	got := wrapText(input, 10, 0)
	// The first line is 16 vis chars + clearLine, so it gets wrapped
	// But wrapText splits at 10, so:
	// "1234567890" + "\033[K\n" + "abcdef\033[K\n" + "xyz"
	expected := "1234567890\033[K\nabcdef\033[K\nxyz"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

// ─── Tests for BuildUsageLineNoTiming ───────────────────────────────────

// hasTiming checks if a line contains timing stats (t=, ttft=, tok/s).
// We check for " t=" (space + t=) to avoid false match on "out=".
func hasTiming(line string) bool {
	return strings.Contains(line, " t=") || strings.Contains(line, "ttft=") || strings.Contains(line, "tok/s")
}

func TestBuildUsageLineNoTiming_Zero(t *testing.T) {
	line := BuildUsageLineNoTiming(stream.Usage{})
	expected := "in=0 out=0"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
	if hasTiming(line) {
		t.Errorf("should not contain timing stats, got %q", line)
	}
}

func TestBuildUsageLineNoTiming_WithCacheRead(t *testing.T) {
	line := BuildUsageLineNoTiming(stream.Usage{Input: 150, Output: 50, CacheRead: 100})
	expected := "in=50 out=50 cin=100"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
}

func TestBuildUsageLineNoTiming_WithReasoning(t *testing.T) {
	line := BuildUsageLineNoTiming(stream.Usage{Input: 200, Output: 100, Reasoning: 50})
	expected := "in=200 out=100"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
}

func TestBuildUsageLineNoTiming_WithCacheWrite(t *testing.T) {
	line := BuildUsageLineNoTiming(stream.Usage{Input: 200, Output: 100, CacheWrite: 25})
	expected := "in=200 out=100 cout=25"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
}

func TestBuildUsageLineNoTiming_AllFields(t *testing.T) {
	line := BuildUsageLineNoTiming(stream.Usage{Input: 500, Output: 300, Reasoning: 50, CacheRead: 100, CacheWrite: 25})
	expected := "in=400 out=300 cin=100 cout=25"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
	if hasTiming(line) {
		t.Errorf("should not contain timing stats, got %q", line)
	}
}

func TestBuildUsageLineNoTiming_CacheReadExceedsInput(t *testing.T) {
	line := BuildUsageLineNoTiming(stream.Usage{Input: 50, Output: 10, CacheRead: 100})
	// in = usage.Input - usage.CacheRead = 50-100 = -10, clamped to 0
	expected := "in=0 out=10 cin=100"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
}

func TestBuildUsageLineNoTiming_NoTimingInOutput(t *testing.T) {
	// Verify that none of the various usage combinations produce timing stats
	tests := []stream.Usage{
		{Input: 0, Output: 0},
		{Input: 100, Output: 50},
		{Input: 100, Output: 50, CacheRead: 30},
		{Input: 100, Output: 50, Reasoning: 20},
		{Input: 100, Output: 50, CacheWrite: 10},
		{Input: 100, Output: 50, CacheRead: 20, Reasoning: 10, CacheWrite: 5},
	}
	for _, u := range tests {
		line := BuildUsageLineNoTiming(u)
		if hasTiming(line) {
			t.Errorf("BuildUsageLineNoTiming(%+v) = %q contains timing stats", u, line)
		}
	}
}
