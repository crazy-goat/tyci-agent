package display

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/decodo/tyci/stream"
	"golang.org/x/term"
)

const minRenderInterval = 100 * time.Millisecond

// Bracketed prefixes — fixed width (6 visible chars) so columns line up.
const (
	prefixRequest  = "[ REQ] "
	prefixThinking = "[THNK] "
	prefixResponse = "[RESP] "
	prefixStat     = "[STAT] "
	prefixTool     = "[TOOL] "
	prefixToolEnd  = "[TOOL} "

	labelToolEnd  = "Tool finish"
	ellipsis      = "..."
	pendingMarker = "⏳"
)

type Minimal struct {
	mu            sync.Mutex
	terminalWidth int

	curPrefix  string
	curContent strings.Builder
	lineActive bool
	lastRender time.Time
	blockStart time.Time

	// curToolName tracks which tool the active [TOOL] line belongs to.
	// Used by ToolCallEnd to ignore stale End calls for earlier tools
	// (the agent emits all Start+Delta first, then all End after tools run).
	curToolName string

	// Tool block state: spans from the first ToolCallStart until ToolFinish().
	toolBlockStart time.Time
	toolBlockOpen  bool

	// done signals the background ticker to exit. Closed by End().
	done chan struct{}
}

func NewMinimal() *Minimal {
	m := &Minimal{
		terminalWidth: getTerminalWidth(),
		blockStart:    time.Now(),
		done:          make(chan struct{}),
	}
	go m.runTicker()
	return m
}

func (m *Minimal) runTicker() {
	ticker := time.NewTicker(minRenderInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			m.tickRender()
		}
	}
}

// tickRender re-renders the active line under the mutex. Called by the
// background ticker so the elapsed time on a pending line updates visibly.
func (m *Minimal) tickRender() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.lineActive {
		return
	}
	now := time.Now()
	if now.Sub(m.lastRender) < minRenderInterval {
		return
	}
	m.lastRender = now
	m.renderLocked(false)
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

// formatElapsed renders a duration as a fixed-width bracketed time string.
// Both the ms and s branches produce exactly 8 visible characters
// ("[  NNNms]" / "[  N.Ns]") so the closing "]" always sits at the same
// column regardless of magnitude. Sub-100ms values use ms; 100ms and
// above switch to seconds with one decimal.
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < 100*time.Millisecond {
		return fmt.Sprintf("[%4dms]", d.Milliseconds())
	}
	return fmt.Sprintf("[%5.1fs]", d.Seconds())
}

func (m *Minimal) width() int {
	w := m.terminalWidth
	if w < 20 {
		w = 20
	}
	return w
}

// fitLine truncates s to maxW visible characters, appending "..." when cut.
func fitLine(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if visibleWidth(s) <= maxW {
		return s
	}
	if maxW <= len(ellipsis) {
		return ellipsis[:maxW]
	}
	runes := []rune(s)
	for len(runes) > 0 && visibleWidth(string(runes))+len(ellipsis) > maxW {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + ellipsis
}

// singleLine returns the curContent collapsed to a single line for display.
func (m *Minimal) singleLine() string {
	raw := m.curContent.String()
	clean := strings.ReplaceAll(raw, "\n", " ")
	for strings.Contains(clean, "  ") {
		clean = strings.ReplaceAll(clean, "  ", " ")
	}
	return strings.TrimSpace(clean)
}

func (m *Minimal) startLine(prefix string) {
	m.curPrefix = prefix
	m.curContent.Reset()
	m.blockStart = time.Now()
	m.lineActive = true
}

// finishLine renders and prints a newline. Caller must hold m.mu.
func (m *Minimal) finishLine() {
	if !m.lineActive {
		return
	}
	m.renderLocked(true)
	fmt.Fprintln(os.Stdout)
	_ = os.Stdout.Sync()
	m.curContent.Reset()
	m.lineActive = false
}

// renderLocked prints the current active line in-place. Caller must hold m.mu.
// The time bracket always sits at the rightmost column of the terminal.
func (m *Minimal) renderLocked(final bool) {
	if !m.lineActive {
		return
	}
	content := m.singleLine()
	elapsed := formatElapsed(time.Since(m.blockStart))
	timeW := visibleWidth(elapsed)
	maxW := m.width() - timeW
	if maxW < 10 {
		maxW = 10
	}

	prefix := m.curPrefix
	bodyAvail := maxW - visibleWidth(prefix)
	if bodyAvail < 1 {
		bodyAvail = 1
	}

	// Pending (empty) lines: show a spinner so user sees liveness.
	if content == "" && !final {
		bodyAvail -= 2
		if bodyAvail < 3 {
			bodyAvail = 3
		}
		spinner := []rune("-/|\\")[int(time.Since(m.blockStart)/minRenderInterval)%4]
		out := prefix + " " + string(spinner)
		if visibleWidth(out) > maxW {
			out = fitLine(out, maxW)
		}
		pad := maxW - visibleWidth(out)
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintf(os.Stdout, "\r%s%s%s", out, strings.Repeat(" ", pad), elapsed)
		_ = os.Stdout.Sync()
		return
	}

	out := prefix + " " + fitLine(content, bodyAvail-1)
	if visibleWidth(out) > maxW {
		out = fitLine(out, maxW)
	}
	pad := maxW - visibleWidth(out)
	if pad < 0 {
		pad = 0
	}
	fmt.Fprintf(os.Stdout, "\r%s%s%s", out, strings.Repeat(" ", pad), elapsed)
	_ = os.Stdout.Sync()
}

func (m *Minimal) maybeThrottle() {
	if !m.lineActive {
		return
	}
	now := time.Now()
	if now.Sub(m.lastRender) < minRenderInterval {
		return
	}
	m.lastRender = now
	m.renderLocked(false)
}

// Request starts the [ REQ] line for the current round. The line stays
// active (updating its elapsed time via the background ticker) until the
// next event — Thinking, Text, ToolCallStart, ToolBlock, etc. — finalizes
// it. End() and Error() also finalize it as a safety net.
func (m *Minimal) Request(content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finishLine()
	m.startLine(prefixRequest)
	if content != "" {
		m.curContent.WriteString(content)
	}
	m.lastRender = time.Time{} // force immediate render
	m.renderLocked(false)
}

func (m *Minimal) feed(prefix, text string) {
	if !m.lineActive || m.curPrefix != prefix {
		m.finishLine()
		m.startLine(prefix)
	}
	m.curContent.WriteString(text)
	m.maybeThrottle()
}

func (m *Minimal) Thinking(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.feed(prefixThinking, text)
}

func (m *Minimal) Text(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.feed(prefixResponse, text)
}

// ToolCallStart opens a new [TOOL] line in `name(` form and keeps it
// active so ToolCallDelta can stream the arguments. The first tool in
// a block also starts the tool-block timer used by ToolFinish.
func (m *Minimal) ToolCallStart(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Close the previous [TOOL] line (if any) with a ")" before starting
	// the next one. This way the closing paren is added by the tool that
	// follows, not by a potentially-stale ToolCallEnd.
	if m.lineActive && m.curPrefix == prefixTool {
		if s := m.curContent.String(); s != "" && s[len(s)-1] != ')' {
			m.curContent.WriteByte(')')
		}
	}
	m.finishLine()
	if !m.toolBlockOpen {
		m.toolBlockStart = time.Now()
		m.toolBlockOpen = true
	}
	m.startLine(prefixTool)
	m.curContent.WriteString(name)
	m.curContent.WriteByte('(')
	m.curToolName = name
	m.lastRender = time.Time{}
	m.renderLocked(false)
}

// ToolCallDelta streams tool arguments onto the current [TOOL] line.
func (m *Minimal) ToolCallDelta(delta string) {
	if delta == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.lineActive || m.curPrefix != prefixTool {
		m.finishLine()
		m.startLine(prefixTool)
		if !m.toolBlockOpen {
			m.toolBlockStart = time.Now()
			m.toolBlockOpen = true
		}
		m.curContent.WriteByte('(')
	}
	m.curContent.WriteString(delta)
	m.maybeThrottle()
}

// ToolCallEnd finalizes the [TOOL] line for the matching tool. Stale
// End calls (e.g. for an earlier tool whose line was already closed by
// the next ToolCallStart) are ignored. The result is intentionally NOT
// rendered — run mode only shows the tool call (name + params).
func (m *Minimal) ToolCallEnd(name string, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.curToolName != name {
		// Stale End call — this line belongs to a different (later) tool.
		return
	}
	if !m.lineActive || m.curPrefix != prefixTool {
		// Line was already finalized (e.g. by End). Nothing to close.
		return
	}
	if s := m.curContent.String(); s == "" || s[len(s)-1] != ')' {
		m.curContent.WriteByte(')')
	}
	m.lastRender = time.Time{}
	m.renderLocked(false)
	m.finishLine()
	m.curToolName = ""
}

// ToolFinish emits the [tool] Tool finish summary line for the current
// tool block. Safe to call when no tool block is open.
func (m *Minimal) ToolFinish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Close the last [TOOL] line (if still open) with a ")" before
	// emitting the summary. This is the safety net for the last tool.
	if m.lineActive && m.curPrefix == prefixTool {
		if s := m.curContent.String(); s != "" && s[len(s)-1] != ')' {
			m.curContent.WriteByte(')')
		}
		m.lastRender = time.Time{}
		m.renderLocked(false)
	}
	m.finishLine()
	if !m.toolBlockOpen {
		return
	}
	dur := time.Since(m.toolBlockStart)
	m.startLine(prefixToolEnd)
	m.curContent.WriteString(labelToolEnd)
	// Override the blockStart so renderLocked() shows the tool-block duration
	// rather than the per-line elapsed.
	m.blockStart = time.Now().Add(-dur)
	m.lastRender = time.Time{}
	m.renderLocked(false)
	m.finishLine()
	m.toolBlockOpen = false
	m.curToolName = ""
}

func (m *Minimal) ToolBlock(msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finishLine()
	if msg == "" {
		return
	}
	if strings.HasPrefix(msg, pendingMarker) {
		// Suppress the "waiting for tools..." indicator — the tool block
		// is represented by the [TOOL] lines and the [tool] Tool finish
		// line, so the placeholder would only add noise.
		return
	}
	m.startLine(prefixToolEnd)
	m.curContent.WriteString(msg)
	m.lastRender = time.Time{}
	m.renderLocked(false)
	m.finishLine()
}

func (m *Minimal) Summary(usage stream.Usage, stats stream.Stats) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finishLine()
	m.startLine(prefixStat)
	m.curContent.WriteString(buildStatLine(usage))
	m.curContent.WriteByte(' ')
	m.curContent.WriteString(buildStatRate(usage, stats))
	// Override blockStart so the right-aligned time shows the round's
	// total duration (stats.Duration) rather than ~0s since startLine().
	if stats.Duration > 0 {
		m.blockStart = time.Now().Add(-stats.Duration)
	}
	m.lastRender = time.Time{}
	m.renderLocked(false)
	m.finishLine()
}

func (m *Minimal) Error(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finishLine()
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

func (m *Minimal) End() {
	m.mu.Lock()
	m.finishLine()
	m.mu.Unlock()
	close(m.done)
}
