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

// wrapRawText wraps content as plain text (no glamour markdown).
func wrapRawText(content string, useBar bool, width int) string {
	maxW := width
	if useBar {
		maxW = width - 2
	}
	if maxW < 10 {
		maxW = 10
	}
	if useBar {
		bar := lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Render("│")
		textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Italic(true)
		var out strings.Builder
		for _, line := range strings.Split(content, "\n") {
			wrapped := wrapText(line, maxW, 0)
			for _, wl := range strings.Split(wrapped, "\n") {
				wl = strings.TrimSuffix(wl, clearLine)
				out.WriteString(bar)
				out.WriteString(" ")
				out.WriteString(textStyle.Render(wl))
				out.WriteString("\n")
			}
		}
		return strings.TrimRight(out.String(), "\n")
	}
	var out strings.Builder
	for _, line := range strings.Split(content, "\n") {
		wrapped := wrapText(line, maxW, 0)
		for _, wl := range strings.Split(wrapped, "\n") {
			wl = strings.TrimSuffix(wl, clearLine)
			out.WriteString(wl)
			out.WriteString("\n")
		}
	}
	return strings.TrimRight(out.String(), "\n")
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
