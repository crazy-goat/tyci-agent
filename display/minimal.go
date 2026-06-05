package display

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/decodo/tyci-agent/stream"
	"golang.org/x/term"
)

const minRenderInterval = 100 * time.Millisecond

type Minimal struct {
	hideThinking  bool
	hideTools     bool
	blockStart    time.Time
	requestStart  time.Time
	terminalWidth int

	curPrefix  string
	curContent strings.Builder
	lineActive bool
	spinIdx    int
	lastRender time.Time

	totalIn      int
	totalOut     int
	totalCacheRd int
	totalCacheWr int
}

func NewMinimal(hideThinking, hideTools bool) *Minimal {
	width := getTerminalWidth()
	return &Minimal{
		hideThinking:  hideThinking,
		hideTools:     hideTools,
		blockStart:    time.Now(),
		requestStart:  time.Now(),
		terminalWidth: width,
	}
}

func (m *Minimal) blockElapsed() string {
	e := time.Since(m.blockStart).Round(time.Millisecond)
	return fmt.Sprintf("[%v]", e)
}

func getTerminalWidth() int {
	if w := os.Getenv("COLUMNS"); w != "" {
		if width, err := strconv.Atoi(w); err == nil && width > 0 {
			return width
		}
	}
	if term.IsTerminal(int(os.Stdout.Fd())) {
		width, _, err := term.GetSize(int(os.Stdout.Fd()))
		if err == nil && width > 0 {
			return width
		}
	}
	return 100
}

func (m *Minimal) throttleRender(spin bool) {
	if !m.lineActive {
		return
	}
	now := time.Now()
	if now.Sub(m.lastRender) < minRenderInterval && m.curContent.Len() > 0 {
		return
	}
	m.lastRender = now
	m.renderLine(spin)
}

func (m *Minimal) displayContent() string {
	raw := m.curContent.String()
	clean := strings.ReplaceAll(raw, "\n", " ")
	for strings.Contains(clean, "  ") {
		clean = strings.ReplaceAll(clean, "  ", " ")
	}
	return strings.TrimSpace(clean)
}

func (m *Minimal) renderLine(spin bool) {
	if !m.lineActive {
		return
	}
	content := m.displayContent()
	if content == "" {
		return
	}
	elapsed := m.blockElapsed()
	maxW := m.terminalWidth - len(elapsed) - 1
	if maxW < 10 {
		maxW = 10
	}

	rawLine := m.curPrefix + " " + content

	var out string
	if spin && len(rawLine) > maxW {
		sc := string("-/|\\"[m.spinIdx%4])
		avail := maxW - len(m.curPrefix) - 3
		if avail < 3 {
			avail = 3
		}
		short := content
		if len(short) > avail {
			short = short[:avail-3] + "..."
		}
		out = m.curPrefix + " " + short + " " + sc
	} else {
		out = rawLine
		if len(out) > maxW {
			out = out[:maxW-3] + "..."
		}
	}

	padding := maxW - len(out)
	if padding < 1 {
		padding = 1
	}
	fmt.Fprintf(os.Stdout, "\r%s%s%s", out, strings.Repeat(" ", padding), elapsed)
	_ = os.Stdout.Sync()
}

func (m *Minimal) finalizeLine() {
	if !m.lineActive {
		return
	}
	content := m.displayContent()
	elapsed := m.blockElapsed()
	maxW := m.terminalWidth - len(elapsed) - 1
	if maxW < 10 {
		maxW = 10
	}

	out := m.curPrefix + " " + content
	if len(out) > maxW {
		out = out[:maxW-3] + "..."
	}

	padding := maxW - len(out)
	if padding < 1 {
		padding = 1
	}
	fmt.Fprintf(os.Stdout, "\r%s%s%s\n", out, strings.Repeat(" ", padding), elapsed)
	_ = os.Stdout.Sync()
	m.curContent.Reset()
	m.lineActive = false
}

func (m *Minimal) startLine(prefix string) {
	m.flushActiveBlock()
	m.curPrefix = prefix
	m.curContent.Reset()
	m.spinIdx = 0
	m.lineActive = true
}

func (m *Minimal) feedContent(prefix string, text string, useSpinner bool) {
	if !m.lineActive {
		m.startLine(prefix)
	} else if m.curPrefix != prefix {
		m.startLine(prefix)
	}
	m.curContent.WriteString(text)
	m.spinIdx++
	m.throttleRender(useSpinner)
}

func (m *Minimal) Text(text string) {
	m.feedContent("Text:", text, false)
}

func (m *Minimal) Thinking(text string) {
	if m.hideThinking {
		return
	}
	m.feedContent("Thinking:", text, true)
}

func (m *Minimal) firstLineContent() string {
	raw := m.curContent.String()
	if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
		return strings.TrimRight(raw[:idx], " \t")
	}
	return raw
}

func (m *Minimal) flushActiveBlock() {
	if !m.lineActive {
		return
	}
	content := m.firstLineContent()
	m.curContent.Reset()
	if content != "" {
		m.curContent.WriteString(content)
	}
	m.finalizeLine()
}

func (m *Minimal) ToolCall(name, args, result string) {
	if m.hideTools {
		return
	}
	parsed := parseArgs(args)
	title, _ := parsed["description"].(string)
	if title == "" {
		title = name
	}
	m.startLine("Tool:")
	m.curContent.WriteString(title)
	m.finalizeLine()
	m.blockStart = time.Now()

	if name != "read" && result != "" {
		m.startLine("Result:")
		m.curContent.WriteString(result)
		m.finalizeLine()
		m.blockStart = time.Now()
	}
}

func (m *Minimal) Summary(usage stream.Usage) {
	m.flushActiveBlock()
	newIn := usage.Input - usage.CacheRead
	if newIn < 0 {
		newIn = 0
	}
	parts := fmt.Sprintf("in=%d out=%d", newIn, usage.Output)
	if usage.CacheRead > 0 || usage.CacheWrite > 0 {
		parts += fmt.Sprintf(" cache_rd=%d cache_wr=%d", usage.CacheRead, usage.CacheWrite)
	}
	m.startLine("Usage:")
	m.curContent.WriteString(parts)
	m.finalizeLine()
}

func (m *Minimal) Error(err error) {
	m.flushActiveBlock()
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

func (m *Minimal) End() {
	m.flushActiveBlock()
}
