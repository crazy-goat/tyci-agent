package readline

import "strings"

func (e *LineEditor) handleKey(k key) bool {
	switch k.special {
	case KeyEnter:
		return true
	case KeyAltEnter:
		e.buffer = append(e.buffer[:e.cursorPos], append([]rune{'\n'}, e.buffer[e.cursorPos:]...)...)
		e.cursorPos++
		return false
	case KeyCtrlC:
		e.buffer = e.buffer[:0]
		e.cursorPos = 0
		e.printCtrlC()
		e.historyPos = len(e.history)
		e.interrupted = true
		return true
	case KeyCtrlD:
		if len(e.buffer) == 0 {
			return true
		}
		if e.cursorPos < len(e.buffer) {
			e.buffer = append(e.buffer[:e.cursorPos], e.buffer[e.cursorPos+1:]...)
		}
		return false
	case KeyBackspace, KeyCtrlH:
		if e.cursorPos > 0 {
			e.buffer = append(e.buffer[:e.cursorPos-1], e.buffer[e.cursorPos:]...)
			e.cursorPos--
		}
		return false
	case KeyDeleteFwd:
		if e.cursorPos < len(e.buffer) {
			e.buffer = append(e.buffer[:e.cursorPos], e.buffer[e.cursorPos+1:]...)
		}
		return false
	case KeyLeft:
		if e.cursorPos > 0 {
			e.cursorPos--
		}
		return false
	case KeyRight:
		if e.cursorPos < len(e.buffer) {
			e.cursorPos++
		}
		return false
	case KeyUp:
		if e.cursorLine() == 0 {
			if e.historyPos > 0 {
				if e.historyPos == len(e.history) {
					e.draft = string(e.buffer)
				}
				e.historyPos--
				e.buffer = []rune(e.history[e.historyPos])
				e.cursorPos = len(e.buffer)
			}
		} else {
			curLine := e.cursorLine()
			curCol := e.cursorCol()
			prevLineStart := e.lineStart(curLine - 1)
			prevLineLen := e.lineEnd(curLine-1) - prevLineStart
			if curCol > prevLineLen {
				e.cursorPos = e.lineEnd(curLine - 1)
			} else {
				e.cursorPos = prevLineStart + curCol
			}
		}
		return false
	case KeyDown:
		if e.cursorLine() >= e.lineCount()-1 {
			if e.historyPos < len(e.history) {
				e.historyPos++
				if e.historyPos == len(e.history) {
					e.buffer = []rune(e.draft)
					e.draft = ""
				} else {
					e.buffer = []rune(e.history[e.historyPos])
				}
				e.cursorPos = len(e.buffer)
			}
		} else {
			curLine := e.cursorLine()
			curCol := e.cursorCol()
			nextLineStart := e.lineStart(curLine + 1)
			nextLineLen := e.lineEnd(curLine+1) - nextLineStart
			if curCol > nextLineLen {
				e.cursorPos = e.lineEnd(curLine + 1)
			} else {
				e.cursorPos = nextLineStart + curCol
			}
		}
		return false
	case KeyHome, KeyCtrlA:
		e.cursorPos = e.lineStart(e.cursorLine())
		return false
	case KeyEnd, KeyCtrlE:
		e.cursorPos = e.lineEnd(e.cursorLine())
		return false
	case KeyCtrlU:
		lineSt := e.lineStart(e.cursorLine())
		if e.cursorPos > lineSt {
			e.buffer = append(e.buffer[:lineSt], e.buffer[e.cursorPos:]...)
			e.cursorPos = lineSt
		}
		return false
	case KeyCtrlK:
		lineEnd := e.lineEnd(e.cursorLine())
		if e.cursorPos < lineEnd {
			e.buffer = append(e.buffer[:e.cursorPos], e.buffer[lineEnd:]...)
		}
		return false
	case KeyCtrlL:
		e.clearScreen()
		return false
	case KeyCtrlR:
		e.enterSearch()
		return false
	case KeyCtrlW:
		if e.cursorPos == 0 {
			return false
		}
		start := e.cursorPos - 1
		for start > 0 && e.buffer[start-1] != ' ' {
			start--
		}
		e.buffer = append(e.buffer[:start], e.buffer[e.cursorPos:]...)
		e.cursorPos = start
		return false
	case KeyEsc:
		return false
	}

	if k.r != 0 {
		e.buffer = append(e.buffer[:e.cursorPos], append([]rune{k.r}, e.buffer[e.cursorPos:]...)...)
		e.cursorPos++
	}
	return false
}

func (e *LineEditor) lines() []string {
	if len(e.buffer) == 0 {
		return []string{""}
	}
	return strings.Split(string(e.buffer), "\n")
}

func (e *LineEditor) lineCount() int {
	n := 1
	for _, r := range e.buffer {
		if r == '\n' {
			n++
		}
	}
	return n
}

func (e *LineEditor) cursorLine() int {
	n := 0
	for i := 0; i < e.cursorPos; i++ {
		if e.buffer[i] == '\n' {
			n++
		}
	}
	return n
}

func (e *LineEditor) cursorCol() int {
	col := 0
	for i := e.cursorPos - 1; i >= 0; i-- {
		if e.buffer[i] == '\n' {
			break
		}
		col++
	}
	return col
}

func (e *LineEditor) lineStart(line int) int {
	if line == 0 {
		return 0
	}
	n := 0
	for i, r := range e.buffer {
		if r == '\n' {
			n++
			if n == line {
				return i + 1
			}
		}
	}
	return len(e.buffer)
}

func (e *LineEditor) lineEnd(line int) int {
	n := 0
	for i, r := range e.buffer {
		if r == '\n' {
			if n == line {
				return i
			}
			n++
		}
	}
	return len(e.buffer)
}
