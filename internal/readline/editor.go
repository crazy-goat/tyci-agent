package readline

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const DefaultMaxEntries = 500

type LineEditor struct {
	history     []string
	maxHistory  int
	historyFile string
	reader      *bufio.Reader
	appendCount int
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
	return &LineEditor{
		history:     history,
		maxHistory:  maxEntries,
		historyFile: historyFile,
		reader:      bufio.NewReader(os.Stdin),
	}, nil
}

func (e *LineEditor) ReadLine() (string, error) {
	line, err := e.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (e *LineEditor) AddHistory(line string) {
	if line == "" {
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

func (e *LineEditor) History() []string {
	return e.history
}

func (e *LineEditor) Close() error {
	if e.historyFile == "" {
		return nil
	}
	return syncHistoryToFile(e.history, e.historyFile, e.maxHistory)
}
