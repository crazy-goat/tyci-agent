package readline

import (
	"fmt"
	"os"
	"strings"
)

func (e *LineEditor) enterSearch() {
	e.searching = true
	e.searchQuery = e.searchQuery[:0]
	e.searchMatch = len(e.history) - 1
	e.searchBuf = make([]rune, len(e.buffer))
	copy(e.searchBuf, e.buffer)
	e.searchCursor = e.cursorPos
	e.searchDir = -1
}

func (e *LineEditor) exitSearch(accept bool) {
	if !accept {
		e.buffer = make([]rune, len(e.searchBuf))
		copy(e.buffer, e.searchBuf)
		e.cursorPos = e.searchCursor
	}
	e.searching = false
	e.searchQuery = nil
	e.searchBuf = nil
}

func (e *LineEditor) searchNextOlder() {
	if len(e.searchQuery) == 0 || len(e.history) == 0 {
		return
	}
	query := strings.ToLower(string(e.searchQuery))
	for i := e.searchMatch - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(e.history[i]), query) {
			e.searchMatch = i
			e.buffer = []rune(e.history[i])
			e.cursorPos = len(e.buffer)
			return
		}
	}
}

func (e *LineEditor) searchNextNewer() {
	if len(e.searchQuery) == 0 || len(e.history) == 0 {
		return
	}
	query := strings.ToLower(string(e.searchQuery))
	for i := e.searchMatch + 1; i < len(e.history); i++ {
		if strings.Contains(strings.ToLower(e.history[i]), query) {
			e.searchMatch = i
			e.buffer = []rune(e.history[i])
			e.cursorPos = len(e.buffer)
			return
		}
	}
}

func (e *LineEditor) searchUpdateQuery(newQuery []rune) {
	e.searchQuery = newQuery
	if len(e.searchQuery) == 0 || len(e.history) == 0 {
		return
	}
	query := strings.ToLower(string(e.searchQuery))
	for i := len(e.history) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(e.history[i]), query) {
			e.searchMatch = i
			e.buffer = []rune(e.history[i])
			e.cursorPos = len(e.buffer)
			return
		}
	}
}

func (e *LineEditor) handleSearchKey(k key) bool {
	switch k.special {
	case KeyEnter:
		e.exitSearch(true)
		return true
	case KeyCtrlC, KeyEsc:
		e.exitSearch(false)
		return false
	case KeyCtrlR:
		e.searchNextOlder()
		return false
	case KeyCtrlP:
		e.searchNextNewer()
		return false
	case KeyBackspace:
		if len(e.searchQuery) > 0 {
			e.searchQuery = e.searchQuery[:len(e.searchQuery)-1]
			e.searchUpdateQuery(e.searchQuery)
		}
		return false
	case KeyRight, KeyLeft:
		e.exitSearch(true)
		return false
	}

	if k.r != 0 && k.r >= 32 {
		e.searchQuery = append(e.searchQuery, k.r)
		e.searchUpdateQuery(e.searchQuery)
	}
	return false
}

func (e *LineEditor) printCtrlC() {
	fmt.Fprint(os.Stdout, "^C\r\n")
}
