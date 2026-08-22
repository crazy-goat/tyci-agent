package display

import (
	"fmt"
	"strings"
)

// Collapsing a block that repeats itself.
//
// A stuck model emits the same short line over and over, and a command can
// print thousands of identical lines legitimately (a test runner, a log). The
// stream-level guard in api/loopguard.go cuts a runaway request off, but by
// then the lines it did produce are in the transcript, and a tool's output was
// never a runaway to begin with — it is simply long.
//
// Either way, forty copies of one line on screen tell you nothing that one
// copy and a count do not, while costing you the whole viewport.
//
// This runs on a block's CONTENT, before rendering, so the rendered string and
// its cached line list stay derived from the same text — the two must agree or
// the viewport's line arithmetic drifts. It runs only when a block settles,
// not while it streams: the incremental wrapper reuses previously wrapped
// lines and rewriting the text underneath it would invalidate that.
const (
	// repeatRunThreshold is how many identical lines it takes to be worth
	// collapsing. Genuine output repeats a line twice or three times; a dozen
	// is a loop or a log.
	repeatRunThreshold = 8

	// repeatKeep is how many copies are shown before the summary line. More
	// than one, so it is obvious what is repeating and in what context.
	repeatKeep = 2

	// repeatMaxLineLen bounds what counts. A long identical line repeated
	// many times is more likely to be real content — a generated fixture, a
	// data table — than noise.
	repeatMaxLineLen = 200

	// blankRunKeep is how many consecutive blank lines survive. A model that
	// pads its reasoning with newlines can otherwise push everything else off
	// the screen; one real session opened a thinking block with eighteen.
	blankRunKeep = 2
)

// collapseRepeatedLines rewrites runs of identical lines as a few copies plus
// a count, and caps runs of blank lines. Text with nothing to collapse is
// returned unchanged, including the same string, so the common path costs one
// scan and no allocation.
func collapseRepeatedLines(content string) string {
	if !hasCollapsibleRun(content) {
		return content
	}

	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))

	for i := 0; i < len(lines); {
		run := runLength(lines, i)
		line := lines[i]

		switch {
		case strings.TrimSpace(line) == "":
			keep := min(run, blankRunKeep)
			for k := 0; k < keep; k++ {
				out = append(out, line)
			}
		case run >= repeatRunThreshold && len(line) <= repeatMaxLineLen:
			for k := 0; k < repeatKeep; k++ {
				out = append(out, line)
			}
			out = append(out, fmt.Sprintf("… and %d more identical lines", run-repeatKeep))
		default:
			for k := 0; k < run; k++ {
				out = append(out, line)
			}
		}
		i += run
	}
	return strings.Join(out, "\n")
}

// runLength counts how many times lines[i] repeats from i onwards.
func runLength(lines []string, i int) int {
	n := 1
	for i+n < len(lines) && lines[i+n] == lines[i] {
		n++
	}
	return n
}

// hasCollapsibleRun is the fast path: it answers the same question as the loop
// above without building anything.
func hasCollapsibleRun(content string) bool {
	if content == "" {
		return false
	}
	prev := ""
	first := true
	run := 0
	for {
		line := content
		if i := strings.IndexByte(content, '\n'); i >= 0 {
			line, content = content[:i], content[i+1:]
		} else {
			content = ""
		}

		if !first && line == prev {
			run++
			blank := strings.TrimSpace(line) == ""
			if blank && run >= blankRunKeep {
				return true
			}
			if !blank && run+1 >= repeatRunThreshold && len(line) <= repeatMaxLineLen {
				return true
			}
		} else {
			run = 0
		}
		prev, first = line, false

		if content == "" {
			return false
		}
	}
}
