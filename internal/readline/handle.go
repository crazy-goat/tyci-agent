package readline

func (e *LineEditor) handleKey(k key) bool {
	switch k.special {
	case KeyEnter:
		return true
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
		if e.historyPos > 0 {
			if e.historyPos == len(e.history) {
				e.draft = string(e.buffer)
			}
			e.historyPos--
			e.buffer = []rune(e.history[e.historyPos])
			e.cursorPos = len(e.buffer)
		}
		return false
	case KeyDown:
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
		return false
	case KeyHome, KeyCtrlA:
		e.cursorPos = 0
		return false
	case KeyEnd, KeyCtrlE:
		e.cursorPos = len(e.buffer)
		return false
	case KeyCtrlU:
		if e.cursorPos > 0 {
			e.buffer = e.buffer[e.cursorPos:]
			e.cursorPos = 0
		}
		return false
	case KeyCtrlK:
		if e.cursorPos < len(e.buffer) {
			e.buffer = e.buffer[:e.cursorPos]
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
