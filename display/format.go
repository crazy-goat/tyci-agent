package display

import (
	"fmt"
	"strings"
	"time"

	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/rivo/uniseg"
)

// stripAnsi removes ANSI escape sequences from a string.
func stripAnsi(s string) string {
	if strings.IndexByte(s, 0x1b) == -1 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	inEscape := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inEscape {
			if escapeFinal(rune(c)) {
				inEscape = false
			}
			continue
		}
		if c == 0x1b {
			inEscape = true
			continue
		}
		b.WriteByte(c)
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

// layoutToken is one grapheme cluster or ANSI sequence in display text.
// ANSI sequences do not affect width. An escape between two runes in the
// same grapheme cluster stays inside that cluster's token.
type layoutToken struct {
	value   string
	width   int
	newline bool
}

type layoutEscape struct {
	offset int
	value  string
}

// layoutTokens groups visible text into grapheme clusters and keeps ANSI
// sequences at their original offsets. uniseg's cluster width is the
// conservative choice: it can count a Devanagari syllable with a matra wider
// than a shaping terminal draws it. Terminals disagree on some cluster
// widths, so this can wrap text early but does not split a cluster.
func layoutTokens(s string) []layoutToken {
	var visible strings.Builder
	var escapes []layoutEscape
	var escape strings.Builder
	inEscape := false
	for _, r := range s {
		if inEscape {
			escape.WriteRune(r)
			if escapeFinal(r) {
				escapes = append(escapes, layoutEscape{offset: visible.Len(), value: escape.String()})
				escape.Reset()
				inEscape = false
			}
			continue
		}
		if r == 0x1b {
			inEscape = true
			escape.WriteRune(r)
			continue
		}
		visible.WriteRune(r)
	}
	if inEscape && escape.Len() > 0 {
		escapes = append(escapes, layoutEscape{offset: visible.Len(), value: escape.String()})
	}

	plain := visible.String()
	tokens := make([]layoutToken, 0, len(plain))
	state, offset, escapeIndex := -1, 0, 0
	for rest := plain; len(rest) > 0; {
		var cluster string
		var width int
		cluster, rest, width, state = uniseg.FirstGraphemeClusterInString(rest, state)
		end := offset + len(cluster)
		var value strings.Builder
		clusterOffset := 0
		for escapeIndex < len(escapes) && escapes[escapeIndex].offset <= end {
			escapeOffset := escapes[escapeIndex].offset - offset
			if escapeOffset < clusterOffset {
				escapeOffset = clusterOffset
			}
			value.WriteString(cluster[clusterOffset:escapeOffset])
			value.WriteString(escapes[escapeIndex].value)
			clusterOffset = escapeOffset
			escapeIndex++
		}
		value.WriteString(cluster[clusterOffset:])
		tokens = append(tokens, layoutToken{
			value:   value.String(),
			width:   width,
			newline: cluster == "\n" || cluster == "\r\n",
		})
		offset = end
	}
	for ; escapeIndex < len(escapes); escapeIndex++ {
		tokens = append(tokens, layoutToken{value: escapes[escapeIndex].value})
	}
	return tokens
}

// cellWidth returns the maximum number of terminal cells in s. It ignores
// ANSI sequences and uses uniseg's grapheme-cluster widths rather than
// summing rune widths.
func cellWidth(s string) int {
	isASCII := true
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x20 || b > 0x7e {
			isASCII = false
			break
		}
	}
	if isASCII {
		return len(s)
	}

	plain := s
	if strings.IndexByte(s, 0x1b) != -1 {
		plain = stripAnsi(s)
	}

	isPlainASCII := true
	for i := 0; i < len(plain); i++ {
		b := plain[i]
		if b < 0x20 || b > 0x7e {
			isPlainASCII = false
			break
		}
	}
	if isPlainASCII {
		return len(plain)
	}

	width, lineWidth := 0, 0
	state := -1
	for rest := plain; len(rest) > 0; {
		var cluster string
		var w int
		cluster, rest, w, state = uniseg.FirstGraphemeClusterInString(rest, state)
		if cluster == "\n" || cluster == "\r\n" {
			if lineWidth > width {
				width = lineWidth
			}
			lineWidth = 0
			continue
		}
		lineWidth += w
	}
	if lineWidth > width {
		width = lineWidth
	}
	return width
}

// wrapText splits long lines into multiple lines to fit within maxWidth.
// startCol is the number of columns already used on the first line (e.g., prompt).
// It preserves ANSI escape sequences and adds clearLine after each newline.
// Each grapheme cluster stays on one line. A cluster that is wider than
// maxWidth goes on its own line because the function cannot split it.
func wrapText(s string, maxWidth, startCol int) string {
	if maxWidth <= 0 {
		return s
	}

	tokens := layoutTokens(s)
	var result strings.Builder
	var lineTokens []layoutToken
	visibleCount := startCol

	flushLine := func() {
		for _, token := range lineTokens {
			result.WriteString(token.value)
		}
		lineTokens = nil
		visibleCount = 0
	}

	for _, token := range tokens {
		if token.newline {
			flushLine()
			result.WriteString(token.value)
			continue
		}
		if visibleCount > 0 && visibleCount+token.width > maxWidth {
			flushLine()
			result.WriteString(clearLine)
			result.WriteByte('\n')
		}
		lineTokens = append(lineTokens, token)
		visibleCount += token.width
	}
	flushLine()
	return result.String()
}

// escapeFinal reports whether b ends an ANSI escape sequence, the
// final bytes handled by the display layout functions.
func escapeFinal(b rune) bool {
	return b == 'm' || b == 'K' || b == 'H' || b == 'J'
}
