package display

import (
	"fmt"
	"strings"
	"time"

	"github.com/crazy-goat/tyci-agent/stream"
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

func fmtRate(tokens int, genDur time.Duration) string {
	if genDur <= 0 {
		return "0.0"
	}
	return fmt.Sprintf("%.1f", float64(tokens)/genDur.Seconds())
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

// clearLine is the ANSI sequence that erases the rest of the line.
const clearLine = "\033[K"

// wrapText splits long lines into multiple lines to fit within maxWidth.
// startCol is the number of columns already used on the first line (e.g., prompt).
// It preserves ANSI escape sequences and adds clearLine after each newline.
func wrapText(s string, maxWidth, startCol int) string {
	if maxWidth <= 0 {
		return s
	}

	// Tokenize: split into visible characters, escape sequences, and newlines.
	type token struct {
		isEscape bool // true for ANSI escape sequences
		value    string
	}
	var tokens []token
	var esc strings.Builder
	inEscape := false
	for _, r := range s {
		if inEscape {
			esc.WriteRune(r)
			if r == 'm' || r == 'K' || r == 'H' || r == 'J' {
				tokens = append(tokens, token{isEscape: true, value: esc.String()})
				esc.Reset()
				inEscape = false
			}
			continue
		}
		if r == 0x1b {
			inEscape = true
			esc.WriteRune(r)
			continue
		}
		if r == '\n' {
			tokens = append(tokens, token{isEscape: false, value: "\n"})
			continue
		}
		tokens = append(tokens, token{isEscape: false, value: string(r)})
	}
	if inEscape && esc.Len() > 0 {
		// Incomplete escape sequence at end - include it anyway
		tokens = append(tokens, token{isEscape: true, value: esc.String()})
		esc.Reset()
	}

	// Assemble output lines from tokens, wrapping at maxWidth visible chars.
	var result strings.Builder
	var lineTokens []token   // tokens for the current output line
	visibleCount := startCol // account for the starting column offset

	flushLine := func() {
		for _, t := range lineTokens {
			result.WriteString(t.value)
		}
		lineTokens = nil
		visibleCount = 0
	}

	for _, tok := range tokens {
		if tok.isEscape {
			// Escape sequences stay with the current line
			lineTokens = append(lineTokens, tok)
			continue
		}
		if tok.value == "\n" {
			flushLine()
			result.WriteByte('\n')
			continue
		}
		// Visible character
		if visibleCount == maxWidth {
			flushLine()
			result.WriteString(clearLine)
			result.WriteByte('\n')
		}
		lineTokens = append(lineTokens, tok)
		visibleCount++
	}
	flushLine()
	return result.String()
}
