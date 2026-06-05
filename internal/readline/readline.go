package readline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

var (
	ErrEOF       = errors.New("EOF")
	ErrInterrupt = errors.New("interrupt")
)

const DefaultMaxEntries = 500

type LineEditor struct {
	history      []string
	historyPos   int
	buffer       []rune
	cursorPos    int
	draft        string
	maxHistory   int
	historyFile  string

	fd        int
	origState *term.State
	rawState  *term.State
	prompt    string

	searching    bool
	searchQuery  []rune
	searchMatch  int
	searchBuf    []rune
	searchCursor int
	searchDir    int

	interrupted bool
	eofRequested bool

	renderLines    int
	lastCursorLine int
}

func New(historyFile string, maxEntries int) (*LineEditor, error) {
	var history []string
	if historyFile != "" {
		var err error
		history, err = loadHistoryFromFile(historyFile, maxEntries)
		if err != nil {
			fmt.Fprintf(os.Stdout, "warning: %v\n", err)
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
		historyPos:  len(history),
		maxHistory:  maxEntries,
		historyFile: historyFile,
		fd:          fd,
		origState:   origState,
		buffer:      make([]rune, 0, 256),
		cursorPos:   0,
	}
	return e, nil
}

func (e *LineEditor) Read(ctx context.Context, prompt string) (string, error) {
	e.prompt = prompt

	if err := e.enableRawMode(); err != nil {
		return "", err
	}
	defer e.disableRawMode()

	e.render()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		key, err := e.readKey()
		if err != nil {
			if errors.Is(err, os.ErrClosed) || errors.Is(err, io.EOF) {
				return "", ErrEOF
			}
			return "", err
		}

		if e.searching {
			done := e.handleSearchKey(key)
			if done {
				line := string(e.buffer)
				e.resetLine()
				fmt.Fprint(os.Stdout, "\r\n")
				return line, nil
			}
		} else {
			done := e.handleKey(key)
			if done {
				if e.interrupted {
					e.interrupted = false
					e.resetLine()
					return "", ErrInterrupt
				}
				if e.eofRequested {
					e.eofRequested = false
					e.resetLine()
					return "", ErrEOF
				}
				line := string(e.buffer)
				e.resetLine()
				fmt.Fprint(os.Stdout, "\r\n")
				return line, nil
			}
		}
		e.render()
	}
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
		e.history = e.history[len(e.history)-e.maxHistory+1:]
	}
	e.history = append(e.history, line)

	if e.historyFile != "" {
		if err := appendLineToFile(e.historyFile, line); err != nil {
			fmt.Fprintf(os.Stdout, "warning: failed to write history: %v\n", err)
			return
		}
	}
}

func (e *LineEditor) History() []string {
	return e.history
}

func (e *LineEditor) resetLine() {
	e.buffer = e.buffer[:0]
	e.cursorPos = 0
	e.interrupted = false
	e.eofRequested = false
	e.historyPos = len(e.history)
	e.searching = false
	e.searchQuery = nil
	e.searchMatch = 0
	e.searchBuf = nil
	e.searchCursor = 0
	e.searchDir = 0
	e.renderLines = 0
	e.lastCursorLine = 0
}
