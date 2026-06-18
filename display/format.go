package display

import (
	"fmt"
	"strings"
	"time"

	"github.com/decodo/tyci/stream"
)

// stripAnsi removes ANSI escape sequences from a string.
func stripAnsi(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if inEscape {
			if r == 'm' || r == 'K' || r == 'H' || r == 'J' {
				inEscape = false
			}
			continue
		}
		if r == 0x1b {
			inEscape = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// runeWidth returns the display width of a rune in a terminal.
// ASCII characters are 1, CJK and emoji are 2.
func runeWidth(r rune) int {
	if r == 0 {
		return 0
	}
	// ASCII printable
	if r < 0x80 {
		return 1
	}
	// CJK and wide characters
	switch {
	case r >= 0x1100 && r <= 0x115F: // Hangul Jamo
		return 2
	case r >= 0x2E80 && r <= 0x4DBF: // CJK Radicals, Kangxi, Ideographic Description, CJK Unified
		return 2
	case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
		return 2
	case r >= 0xA000 && r <= 0xA4CF: // Yi
		return 2
	case r >= 0xAC00 && r <= 0xD7AF: // Hangul Syllables
		return 2
	case r >= 0xF900 && r <= 0xFAFF: // CJK Compatibility Ideographs
		return 2
	case r >= 0xFE10 && r <= 0xFE19: // Vertical forms
		return 2
	case r >= 0xFE30 && r <= 0xFE6F: // CJK Compatibility Forms
		return 2
	case r >= 0xFF01 && r <= 0xFF60: // Fullwidth Forms
		return 2
	case r >= 0xFFE0 && r <= 0xFFE6: // Fullwidth Signs
		return 2
	case r >= 0x1F000 && r <= 0x1F9FF: // Emoji blocks
		return 2
	case r >= 0x20000 && r <= 0x2FFFF: // CJK Extension B and C
		return 2
	case r >= 0x30000 && r <= 0x3FFFF: // CJK Extension G, H
		return 2
	}
	return 1
}

// visibleWidth returns the visible width of a string (excluding ANSI escapes).
func visibleWidth(s string) int {
	w := 0
	for _, r := range stripAnsi(s) {
		w += runeWidth(r)
	}
	return w
}

func fmtRate(tokens int, genDur time.Duration) string {
	if genDur <= 0 {
		return "0.0"
	}
	return fmt.Sprintf("%.1f", float64(tokens)/genDur.Seconds())
}

// buildStatLine returns the body of the run-mode [STAT] line — just the
// field list, no time and no rate. The elapsed time is rendered on the
// right by Minimal's renderLocked() machinery (so columns stay aligned
// with the other lines) and the caller is responsible for setting
// blockStart so that elapsed equals the round's total duration.
func buildStatLine(usage stream.Usage) string {
	inNew := usage.Input - usage.CacheRead
	if inNew < 0 {
		inNew = 0
	}
	parts := fmt.Sprintf("in=%d", inNew)
	if usage.CacheRead > 0 {
		parts += fmt.Sprintf(" (+%d cache)", usage.CacheRead)
	}
	parts += fmt.Sprintf(" out=%d", usage.Output)
	if usage.Reasoning > 0 {
		parts += fmt.Sprintf(" r=%d", usage.Reasoning)
	}
	if usage.CacheWrite > 0 {
		parts += fmt.Sprintf(" cache_w=%d", usage.CacheWrite)
	}
	return parts
}

// buildStatRate returns the "tok/s=N.N" suffix for the [STAT] line.
// Exposed separately so the caller can compute it from the same
// stats.Duration that drives the right-aligned time, keeping the
// displayed rate and the displayed time in lockstep.
func buildStatRate(usage stream.Usage, stats stream.Stats) string {
	var rate float64
	if stats.Duration > 0 {
		rate = float64(usage.Output) / stats.Duration.Seconds()
	}
	return fmt.Sprintf("tok/s=%.1f", rate)
}

// buildUsageLine returns the full usage line with tokens and timing concatenated.
// Kept for backward compatibility (used by Minimal display).
func buildUsageLine(usage stream.Usage, stats stream.Stats) string {
	return usageTokens(usage) + " " + timingTokens(usage, stats)
}

// usageTokens builds the token-count part (left side).
// in= shows actual new input tokens (total minus cache read), with cache read in brackets.
// ctx= shows total context (Input + Output).
func usageTokens(usage stream.Usage) string {
	inActual := usage.Input - usage.CacheRead
	if inActual < 0 {
		inActual = 0
	}
	parts := fmt.Sprintf("in=%d", inActual)
	if usage.CacheRead > 0 {
		parts += fmt.Sprintf("[%d]", usage.CacheRead)
	}
	parts += fmt.Sprintf(" out=%d", usage.Output)
	if usage.CacheWrite > 0 {
		parts += fmt.Sprintf("[%d]", usage.CacheWrite)
	}
	if usage.Reasoning > 0 {
		parts += fmt.Sprintf(" r=%d", usage.Reasoning)
	}
	parts += fmt.Sprintf(" ctx=%d", usage.Input+usage.Output)
	return parts
}

// timingTokens builds the timing part (right side).
func timingTokens(usage stream.Usage, stats stream.Stats) string {
	genDur := stats.Duration - stats.FirstToken
	if genDur < 0 {
		genDur = 0
	}
	return fmt.Sprintf("t=%.1fs ttft=%.2fs tok/s=%s",
		stats.Duration.Seconds(),
		stats.FirstToken.Seconds(),
		fmtRate(usage.Output, genDur),
	)
}

// buildAlignedUsageLine builds a line with token info left-aligned and timing
// info right-aligned, padded to fill the given width.
// If the terminal is too narrow for alignment, falls back to concatenation.
func buildAlignedUsageLine(width int, prefix string, usage stream.Usage, stats stream.Stats) string {
	left := prefix + " " + usageTokens(usage)
	right := timingTokens(usage, stats)
	if width >= visibleWidth(left)+visibleWidth(right) {
		return left + strings.Repeat(" ", width-visibleWidth(left)-visibleWidth(right)) + right
	}
	// Narrow terminal: just concatenate with one space
	return left + " " + right
}

// buildCostsLine formats cumulative session token counts.
// Shows total tokens across all iterations in the session.
// Order: main counts (in, out), then cache details (cin, cout).
func buildCostsLine(usage stream.Usage) string {
	inActual := usage.Input - usage.CacheRead
	if inActual < 0 {
		inActual = 0
	}
	parts := fmt.Sprintf("in=%d", inActual)
	parts += fmt.Sprintf(" out=%d", usage.Output)
	if usage.CacheRead > 0 {
		parts += fmt.Sprintf(" cin=%d", usage.CacheRead)
	}
	if usage.CacheWrite > 0 {
		parts += fmt.Sprintf(" cout=%d", usage.CacheWrite)
	}
	return parts
}

// BuildUsageLineNoTiming formats usage without timing stats.
// Used for session totals where timing (per-request) is not meaningful.
// in= shows actual new input tokens (total minus cache read).
// Order: main counts (in, out), then cache details (cin, cout).
func BuildUsageLineNoTiming(usage stream.Usage) string {
	inActual := usage.Input - usage.CacheRead
	if inActual < 0 {
		inActual = 0
	}
	parts := fmt.Sprintf("in=%d", inActual)
	parts += fmt.Sprintf(" out=%d", usage.Output)
	if usage.CacheRead > 0 {
		parts += fmt.Sprintf(" cin=%d", usage.CacheRead)
	}
	if usage.CacheWrite > 0 {
		parts += fmt.Sprintf(" cout=%d", usage.CacheWrite)
	}
	return parts
}
