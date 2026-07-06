package display

import "strings"

// collapseMarkdownBlankLines normalizes the blank lines produced by glamour.
//
// glamour emits padding/separator lines that, after ANSI stripping, contain
// only whitespace. It also does not collapse runs of blank lines the way the
// streaming raw wrapper does. Left alone this makes the force-rendered output
// grow by N blank lines relative to what streamWrap just displayed, causing
// cachedLineCount / cachedLines to disagree with the streaming form and the
// user to see stacks of empty rows.
//
// This function collapses any run of 2+ consecutive blank lines down to a
// single blank line. A line is "blank" when its ANSI-stripped form has no
// non-whitespace characters. The ORIGINAL bytes (including ANSI color codes)
// of every kept line are preserved so the display still receives correctly
// styled output — only surplus blank lines are dropped.
//
// The input is expected to already be trimmed with strings.Trim(out, "\n").
func collapseMarkdownBlankLines(rendered string) string {
	if rendered == "" {
		return ""
	}
	lines := strings.Split(rendered, "\n")
	isBlank := make([]bool, len(lines))
	for i, line := range lines {
		// plainLine strips ANSI (and a trailing clearLine sequence).
		isBlank[i] = strings.TrimSpace(plainLine(line)) == ""
	}
	out := make([]string, 0, len(lines))
	prevBlank := false
	for i, line := range lines {
		if isBlank[i] {
			if prevBlank {
				continue
			}
			out = append(out, line)
			prevBlank = true
		} else {
			out = append(out, line)
			prevBlank = false
		}
	}
	return strings.Join(out, "\n")
}
