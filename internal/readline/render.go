package readline

import (
	"fmt"
	"os"
	"strings"
)

const continuation = "  "

func (e *LineEditor) render() {
	if e.renderLines > 0 {
		fmt.Fprintf(os.Stdout, "\033[%dA", e.lastCursorLine)
		fmt.Fprint(os.Stdout, "\r\033[J")
	}

	if e.searching {
		fmt.Fprintf(os.Stdout, "(reverse-i-search)`%s': %s", string(e.searchQuery), string(e.buffer))
		e.renderLines = 1
		e.lastCursorLine = 0
		return
	}

	lines := e.lines()
	for i, line := range lines {
		if i > 0 {
			fmt.Fprint(os.Stdout, "\r\n")
		}
		if i == 0 {
			fmt.Fprint(os.Stdout, e.prompt)
		} else {
			fmt.Fprint(os.Stdout, continuation)
		}
		fmt.Fprint(os.Stdout, line)
	}

	curLine := e.cursorLine()
	curCol := e.cursorCol()

	if curLine < len(lines)-1 {
		fmt.Fprintf(os.Stdout, "\033[%dA", len(lines)-1-curLine)
	}
	col := curCol
	if curLine == 0 {
		col += len([]rune(e.prompt))
	} else {
		col += len([]rune(continuation))
	}
	if col > 0 {
		fmt.Fprintf(os.Stdout, "\033[%dC", col)
	} else {
		fmt.Fprint(os.Stdout, "\r")
	}

	e.renderLines = len(lines)
	e.lastCursorLine = curLine
}

func (e *LineEditor) moveCursor() {
	pos := 0
	if !e.searching {
		pos = len([]rune(e.prompt)) + e.cursorPos
	} else {
		searchPrompt := fmt.Sprintf("(reverse-i-search)`%s': ", string(e.searchQuery))
		pos = len([]rune(searchPrompt)) + e.cursorPos
	}
	if pos > 0 {
		fmt.Fprintf(os.Stdout, "\r\033[%dC", pos)
	} else {
		fmt.Fprint(os.Stdout, "\r")
	}
}

func (e *LineEditor) clearScreen() {
	fmt.Fprint(os.Stdout, "\033[2J\033[H")
}

func visibleWidth(s string) int {
	return len([]rune(s))
}

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
