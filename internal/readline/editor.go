package readline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/term"
)

var (
	ErrEOF       = errors.New("EOF")
	ErrInterrupt = errors.New("interrupt")
)

const DefaultMaxEntries = 500

type LineEditor struct {
	history     []string
	maxHistory  int
	historyFile string
	historyIdx  int
	appendCount int

	mu         sync.Mutex
	state      *term.State
	fd         int
	origState  *term.State
	prompt     string
	buffer     []rune
	cursor     int
	historyNav bool
	searchMode bool
	searchBuf  []rune
	searchIdx  int
	searchDir  int
}

func New(historyFile string, maxEntries int) (*LineEditor, error) {
	var history []string
	if historyFile != "" {
		var err error
		history, err = loadHistoryFromFile(historyFile, maxEntries)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			history = nil
		}
		history = dedupAdjacent(history)
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, fmt.Errorf("not a terminal")
	}

	origState, err := term.GetState(fd)
	if err != nil {
		return nil, fmt.Errorf("get terminal state: %w", err)
	}

	e := &LineEditor{
		history:     history,
		maxHistory:  maxEntries,
		historyFile: historyFile,
		historyIdx:  len(history),
		fd:          fd,
		origState:   origState,
		buffer:      make([]rune, 0, 256),
		cursor:      0,
	}
	return e, nil
}

func (e *LineEditor) Read(ctx context.Context, prompt string) (string, error) {
	e.prompt = prompt

	if err := e.enableRawMode(); err != nil {
		return "", err
	}
	defer e.disableRawMode()

	e.drawPrompt()

	ch := make(chan struct {
		r   rune
		err error
	}, 1)

	go func() {
		buf := make([]byte, 4)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				ch <- struct {
					r   rune
					err error
				}{0, err}
				return
			}
			for i := 0; i < n; {
				r, size := utf8.DecodeRune(buf[i:])
				i += size
				ch <- struct {
					r   rune
					err error
				}{r, nil}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case c := <-ch:
			if c.err != nil {
				if errors.Is(c.err, os.ErrClosed) || errors.Is(c.err, io.EOF) {
					return "", ErrEOF
				}
				return "", c.err
			}
			if e.handleKey(c.r) {
				line := string(e.buffer)
				e.buffer = e.buffer[:0]
				e.cursor = 0
				e.historyIdx = len(e.history)
				e.historyNav = false
				fmt.Fprint(os.Stdout, "\r\n")
				return line, nil
			}
			e.redraw()
		}
	}
}

func (e *LineEditor) enableRawMode() error {
	state, err := term.MakeRaw(e.fd)
	if err != nil {
		return err
	}
	e.state = state
	return nil
}

func (e *LineEditor) disableRawMode() error {
	if e.state != nil {
		return term.Restore(e.fd, e.state)
	}
	return nil
}

func (e *LineEditor) Close() error {
	if e.historyFile == "" {
		return nil
	}
	return syncHistoryToFile(e.history, e.historyFile, e.maxHistory)
}

func (e *LineEditor) AddHistory(line string) {
	if line == "" || line == "/exit" {
		return
	}
	if len(e.history) > 0 && e.history[len(e.history)-1] == line {
		return
	}
	if len(e.history) >= e.maxHistory {
		e.history = e.history[1:]
	}
	e.history = append(e.history, line)

	if e.historyFile != "" {
		if err := appendLineToFile(e.historyFile, line); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write history: %v\n", err)
			return
		}
		e.appendCount++
		if e.appendCount >= e.maxHistory {
			if err := syncHistoryToFile(e.history, e.historyFile, e.maxHistory); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to rotate history: %v\n", err)
			}
			e.appendCount = 0
		}
	}
}

func (e *LineEditor) handleKey(r rune) bool {
	if e.searchMode {
		return e.handleSearchKey(r)
	}

	switch r {
	case 3: // Ctrl+C
		e.searchMode = false
		e.searchBuf = nil
		e.buffer = e.buffer[:0]
		e.cursor = 0
		e.redraw()
		return false
	case 4: // Ctrl+D
		if len(e.buffer) == 0 {
			fmt.Fprint(os.Stdout, "\r\n")
			return true // EOF
		}
	case 13, 10: // Enter
		return true
	case 127, 8: // Backspace / Delete
		if e.cursor > 0 {
			e.buffer = append(e.buffer[:e.cursor-1], e.buffer[e.cursor:]...)
			e.cursor--
		}
	case 27: // Escape sequence
		// We'll read more bytes in the next iteration
		return false
	case '[': // Could be escape sequence, but we handle it in sequence
		return false
	case 'A': // Up arrow (part of escape)
		if e.historyNav || len(e.history) > 0 {
			e.historyNav = true
			if e.historyIdx > 0 {
				e.historyIdx--
				e.buffer = []rune(e.history[e.historyIdx])
				e.cursor = len(e.buffer)
			}
		}
		return false
	case 'B': // Down arrow
		if e.historyNav {
			if e.historyIdx < len(e.history)-1 {
				e.historyIdx++
				e.buffer = []rune(e.history[e.historyIdx])
				e.cursor = len(e.buffer)
			} else {
				e.historyIdx = len(e.history)
				e.buffer = e.buffer[:0]
				e.cursor = 0
				e.historyNav = false
			}
		}
		return false
	case 'C': // Right arrow
		if e.cursor < len(e.buffer) {
			e.cursor++
		}
		return false
	case 'D': // Left arrow
		if e.cursor > 0 {
			e.cursor--
		}
		return false
	case 'H': // Home
		e.cursor = 0
		return false
	case 'F': // End
		e.cursor = len(e.buffer)
		return false
	case 18: // Ctrl+R - reverse search
		e.searchMode = true
		e.searchBuf = e.searchBuf[:0]
		e.searchIdx = len(e.history)
		e.searchDir = -1
		return false
	default:
		if r >= 32 && r != 127 {
			e.buffer = append(e.buffer[:e.cursor], append([]rune{r}, e.buffer[e.cursor:]...)...)
			e.cursor++
		}
	}
	return false
}

func (e *LineEditor) handleSearchKey(r rune) bool {
	switch r {
	case 18: // Ctrl+R - next match
		e.findNextSearchMatch()
		return false
	case 16: // Ctrl+P - previous match (or Ctrl+N for next)
		e.searchDir = 1
		e.findNextSearchMatch()
		return false
	case 13, 10: // Enter - accept search result
		e.searchMode = false
		e.searchBuf = nil
		return false
	case 3: // Ctrl+C - cancel search
		e.searchMode = false
		e.searchBuf = nil
		e.buffer = e.buffer[:0]
		e.cursor = 0
		return false
	case 127, 8: // Backspace
		if len(e.searchBuf) > 0 {
			e.searchBuf = e.searchBuf[:len(e.searchBuf)-1]
			e.searchIdx = len(e.history)
		}
		return false
	case 27: // Escape
		e.searchMode = false
		e.searchBuf = nil
		return false
	default:
		if r >= 32 && r != 127 {
			e.searchBuf = append(e.searchBuf, r)
			e.searchIdx = len(e.history)
			e.findNextSearchMatch()
		}
		return false
	}
}

func (e *LineEditor) findNextSearchMatch() {
	if len(e.searchBuf) == 0 {
		return
	}
	searchStr := string(e.searchBuf)
	start := e.searchIdx
	if e.searchDir == -1 {
		for i := start - 1; i >= 0; i-- {
			if strings.Contains(e.history[i], searchStr) {
				e.searchIdx = i
				e.buffer = []rune(e.history[i])
				e.cursor = len(e.buffer)
				e.historyNav = true
				e.historyIdx = i
				return
			}
		}
	} else {
		for i := start + 1; i < len(e.history); i++ {
			if strings.Contains(e.history[i], searchStr) {
				e.searchIdx = i
				e.buffer = []rune(e.history[i])
				e.cursor = len(e.buffer)
				e.historyNav = true
				e.historyIdx = i
				return
			}
		}
	}
}

func (e *LineEditor) redraw() {
	fmt.Fprint(os.Stdout, "\r\033[K")
	if e.searchMode {
		fmt.Fprintf(os.Stdout, "(reverse-i-search)`%s': %s", string(e.searchBuf), string(e.buffer))
	} else {
		fmt.Fprint(os.Stdout, e.prompt)
		fmt.Fprint(os.Stdout, string(e.buffer))
	}
	e.moveCursor()
}

func (e *LineEditor) drawPrompt() {
	fmt.Fprint(os.Stdout, e.prompt)
}

func (e *LineEditor) moveCursor() {
	pos := len(e.prompt) + e.cursor
	fmt.Fprintf(os.Stdout, "\r\033[%dC", pos)
}

func (e *LineEditor) History() []string {
	return e.history
}