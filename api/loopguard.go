package api

import (
	"fmt"
	"strings"
)

// Detection of a model that has stopped making progress.
//
// A degenerate model emits the same short line over and over: one real session
// spent 78 seconds of "thinking" producing several hundred copies of
// "</invoke>" and would have carried on to its token limit. Nothing downstream
// notices — the stream is well-formed, the request is valid, the provider is
// happy to keep billing for it — so the damage is paid for three times: the
// wall-clock, the tokens, and the transcript, which keeps those hundreds of
// lines and feeds them back on every later request.
//
// Cutting the stream is the only useful response. The agent already knows what
// to do with a failed request: try the next fallback model, and tell the user
// if there is none.
const (
	// maxRepeatedLines is how many consecutive identical lines are tolerated
	// before the stream is treated as stuck. High enough that no real answer
	// reaches it — repeated lines in genuine output come in twos and threes,
	// not dozens — and low enough to stop the loop while it is still cheap.
	maxRepeatedLines = 24

	// maxRepeatedLineLen bounds what counts as a repeat. A long identical
	// line repeated many times is far more likely to be real content (a data
	// table, a generated fixture) than a stuck decoder.
	maxRepeatedLineLen = 120
)

// repeatGuard watches one delta stream for a line repeating without end.
//
// Deltas do not arrive on line boundaries, so complete lines are assembled
// here rather than assumed. Blank lines neither count nor reset: a stuck model
// commonly alternates its line with an empty one, and treating that as
// progress would defeat the whole check.
type repeatGuard struct {
	pending string
	last    string
	count   int
}

// Feed returns a non-nil error once the stream has clearly stopped
// progressing. The caller should abandon the request.
func (g *repeatGuard) Feed(delta string) error {
	g.pending += delta
	for {
		i := strings.IndexByte(g.pending, '\n')
		if i < 0 {
			return nil
		}
		line := strings.TrimSpace(g.pending[:i])
		g.pending = g.pending[i+1:]

		if line == "" || len(line) > maxRepeatedLineLen {
			continue
		}
		if line != g.last {
			g.last = line
			g.count = 1
			continue
		}
		g.count++
		if g.count >= maxRepeatedLines {
			return fmt.Errorf("model stopped making progress: it repeated the same line %d times (%q) — the request was cut off",
				g.count, truncateForMessage(line, 60))
		}
	}
}

func truncateForMessage(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
