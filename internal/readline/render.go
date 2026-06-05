package readline

import (
	"fmt"
	"os"
	"strings"
)

func (e *LineEditor) render() {
	fmt.Fprint(os.Stdout, "\r\033[K")
	if e.searching {
		fmt.Fprintf(os.Stdout, "(reverse-i-search)`%s': %s", string(e.searchQuery), string(e.buffer))
	} else {
		fmt.Fprint(os.Stdout, e.prompt)
		fmt.Fprint(os.Stdout, string(e.buffer))
	}
	e.moveCursor()
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
