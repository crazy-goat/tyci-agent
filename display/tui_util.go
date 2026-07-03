package display

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// lineCount returns the number of newline-delimited lines in s.
func lineCount(s string) int {
	n := 1
	for _, r := range s {
		if r == '\n' {
			n++
		}
	}
	return n
}

// rawWrapper wraps logical text lines into display sub-lines, optionally
// prefixed with a colored bar. Styles are built once per wrapper instead of
// per line.
type rawWrapper struct {
	useBar bool
	maxW   int
	bar    string
	style  lipgloss.Style
}

func newRawWrapper(useBar bool, width int) rawWrapper {
	maxW := width
	if useBar {
		maxW = width - 2
	}
	if maxW < 10 {
		maxW = 10
	}
	w := rawWrapper{useBar: useBar, maxW: maxW}
	if useBar {
		w.bar = lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Render("│")
		w.style = lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Italic(true)
	}
	return w
}

// appendLogicalLine wraps one logical (newline-free) line and appends the
// resulting display sub-lines to lines.
func (w rawWrapper) appendLogicalLine(lines []string, line string) []string {
	wrapped := wrapText(line, w.maxW, 0)
	for _, wl := range strings.Split(wrapped, "\n") {
		wl = strings.TrimSuffix(wl, clearLine)
		if w.useBar {
			wl = w.bar + " " + w.style.Render(wl)
		}
		lines = append(lines, wl)
	}
	return lines
}

// wrapRawText wraps content as plain text (no glamour markdown).
func wrapRawText(content string, useBar bool, width int) string {
	w := newRawWrapper(useBar, width)
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		lines = w.appendLogicalLine(lines, line)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// ─── History persistence ──────────────────────────────────────────────────

// loadTuiHistory reads history lines from a file.
func loadTuiHistory(path string) []string {
	if path == "" {
		return make([]string, 0, 128)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return make([]string, 0, 128)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// Filter printable lines and trim
	clean := make([]string, 0, len(lines))
	for _, l := range lines {
		if l != "" && isPrintable(l) {
			clean = append(clean, l)
		}
	}
	// Cap at max
	if len(clean) > tuiMaxHistory {
		clean = clean[len(clean)-tuiMaxHistory:]
	}
	return clean
}

// appendTuiHistory appends a single line to the history file.
func appendTuiHistory(path, line string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}

// isPrintable checks if a string contains only printable characters.
func isPrintable(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\t' {
			return false
		}
	}
	return true
}
