package display

import (
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/stream"
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
	// 💭 is one rune (visible width 1), so 10 visible chars: "💭123456789"
	expected := "💭123456789\033[K\n0abcdef"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
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
