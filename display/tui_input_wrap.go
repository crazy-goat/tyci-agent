package display

import (
	"strings"
	"unicode"

	rw "github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// inputWrappedRows returns the number of visual rows the textarea value
// occupies at m.input.Width(): for each logical line it sums the length of
// its soft-wrap, exactly like the textarea renders it.
//
// bubbles v1.0.0 LineCount() only counts logical lines (hard newlines), so it
// cannot drive capInputHeight: a long single-line prompt stays height 1 and
// the viewport scrolls away instead of growing downward. This is the same sum
// cursorLineNumber performs over m.memoizedWrap — but wrap itself is private,
// so it is reproduced here 1:1 (verbatim copy of bubbles/textarea.wrap,
// bubbles@v1.0.0 textarea/textarea.go). Do not "simplify" it: the trailing
// space handling and the double-width-rune edge are load-bearing; a plain
// space-split wrap disagrees with the widget and reintroduces the bug.
//
// Called multiple times per keystroke (capInputHeight and friends), so it
// caches its result keyed on (len(value), width): typing a run of ordinary
// characters changes the length every call, but repeated calls within the
// same keystroke handler — or navigation that doesn't change the text —
// hit the cache and skip the O(n) wrap. This is a length-based cache, not a
// content hash, so it can theoretically miss a change that swaps runes
// without changing the total count; that only matters if it under-caches
// (recomputes when unneeded), which is safe — it never returns a stale
// result for a still-current length+width pair because both need to match.
func (m *TuiModel) inputWrappedRows() int {
	value := m.input.Value()
	width := m.input.Width()
	if m.wrapCacheValid && m.wrapCacheLen == len(value) && m.wrapCacheWidth == width {
		return m.wrapCacheRows
	}
	rows := 0
	for _, line := range strings.Split(value, "\n") {
		rows += len(wrapRunes([]rune(line), width))
	}
	m.wrapCacheValid = true
	m.wrapCacheLen = len(value)
	m.wrapCacheWidth = width
	m.wrapCacheRows = rows
	return rows
}

// wrapRunes is a verbatim copy of bubbles/textarea.wrap
// (github.com/charmbracelet/bubbles@v1.0.0 textarea/textarea.go), including
// its trailing-space handling at end-of-text and the double-width-rune edge.
// The original is private, so the sum over wrapped rows that cursorLineNumber
// computes cannot be read off the public API; this copy must stay 1:1 with it
// or the input height drifts from what the textarea actually renders.
func wrapRunes(runes []rune, width int) [][]rune {
	if width < 1 {
		width = 1
	}

	var (
		lines  = [][]rune{{}}
		word   = []rune{}
		row    int
		spaces int
	)

	// Word wrap the runes
	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 { //nolint:nestif
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
				spaces = 0
				word = nil
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
				spaces = 0
				word = nil
			}
		} else {
			// If the last character is a double-width rune, then we may not be able to add it to this line
			// as it might cause us to go past the width.
			lastCharLen := rw.RuneWidth(word[len(word)-1])
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				// If the current line has any content, let's move to the next
				// line because the current word fills up the entire line.
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		// We add an extra space at the end of the line to account for the
		// trailing space at the end of the previous soft-wrapped lines so that
		// behaviour when navigating is consistent and so that we don't need to
		// continually add edges to handle the last line of the wrapped input.
		spaces++
		lines[row+1] = append(lines[row+1], repeatSpaces(spaces)...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], repeatSpaces(spaces)...)
	}

	return lines
}

func repeatSpaces(n int) []rune {
	return []rune(strings.Repeat(string(' '), n))
}
