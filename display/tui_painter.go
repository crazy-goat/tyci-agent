package display

import (
	"bytes"
	"io"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// painter is a minimal, event-driven terminal renderer used in place of
// bubbletea's ticker-based standard renderer (bubbletea runs with
// tea.WithoutRenderer so the painter owns the terminal).
//
// bubbletea's standard renderer wakes a goroutine at the configured framerate
// even when nothing changes, which sets a small but nonzero idle-CPU floor and
// adds up to 1/fps of latency before a key press is echoed. This painter is
// driven by View() instead: it paints only when a new frame is produced, so an
// idle TUI performs zero wakeups (the event loop simply blocks on its message
// channel) and key presses repaint immediately.
//
// The frame diff mirrors bubbletea's alt-screen flush(): move home, compare the
// new frame line-by-line against the last, skip unchanged lines by advancing the
// cursor, and rewrite only the lines that changed. Because View() runs on the
// single event-loop goroutine, all writes are serialized without extra locking;
// the mutex only guards start()/stop(), which run from the program goroutine.
type painter struct {
	mu    sync.Mutex
	out   io.Writer
	mouse bool

	started       bool
	needsClear    bool // force a full clear+repaint on the next paint (e.g. after resize)
	lastFrame     string
	lastLines     []string
	linesRendered int
}

func newPainter(out io.Writer, mouse bool) *painter {
	return &painter{out: out, mouse: mouse}
}

// enter puts the terminal into the state the painter expects: alternate screen,
// bracketed paste, optional mouse tracking, hidden cursor, cleared screen. It
// runs lazily on the first paint so it executes on the event-loop goroutine
// after bubbletea has already switched stdin into raw mode.
func (p *painter) enter() {
	var b strings.Builder
	b.WriteString(ansi.SetMode(ansi.AltScreenBufferMode))
	b.WriteString(ansi.SetMode(ansi.BracketedPasteMode))
	if p.mouse {
		b.WriteString(ansi.SetMode(ansi.MouseCellMotionMode, ansi.MouseSgrExtMode))
	}
	b.WriteString(ansi.HideCursor)
	b.WriteString(ansi.EraseEntireScreen)
	b.WriteString(ansi.CursorHomePosition)
	_, _ = io.WriteString(p.out, b.String())
	p.started = true
	p.needsClear = false
	p.linesRendered = 0
	p.lastFrame = ""
	p.lastLines = nil
}

// stop restores the terminal to its pre-TUI state. It is safe to call more than
// once and is a no-op if the painter never entered.
func (p *painter) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return
	}
	var b strings.Builder
	b.WriteString(ansi.ShowCursor)
	if p.mouse {
		b.WriteString(ansi.ResetMode(ansi.MouseCellMotionMode, ansi.MouseSgrExtMode))
	}
	b.WriteString(ansi.ResetMode(ansi.BracketedPasteMode))
	b.WriteString(ansi.ResetMode(ansi.AltScreenBufferMode))
	_, _ = io.WriteString(p.out, b.String())
	p.started = false
}

// repaint forces the next paint to redraw the whole screen. Call it when the
// prior frame's geometry is no longer valid, e.g. after a terminal resize.
func (p *painter) repaint() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.needsClear = true
	p.lastFrame = ""
	p.lastLines = nil
}

// paint renders frame with no scroll-region optimization. It is kept for
// callers (and tests) that don't have a scrollable region to hint.
func (p *painter) paint(frame string, width, height int) {
	p.paintRegion(frame, width, height, 0)
}

// paintRegion renders frame to the terminal, writing only the lines that differ
// from the previously painted frame. width/height are the current terminal size
// and are used to truncate overlong lines and to clamp the frame to the visible
// screen (dropping lines from the top, matching bubbletea, since we cannot move
// the cursor into scrollback).
//
// scrollBottom, when > 1, marks the message region as rows [0, scrollBottom):
// if the new frame is that region scrolled up (a streaming log growing at the
// bottom) the painter scrolls it in hardware (DECSTBM scroll region + SU) so the
// line diff only repaints the newly revealed rows instead of every row. Pass 0
// to disable this (e.g. for full-screen overlays that don't scroll).
func (p *painter) paintRegion(frame string, width, height, scrollBottom int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Until the real terminal size is known (the first WindowSizeMsg), the frame
	// is built at width 0 and is meaningless; skip painting it so the first
	// visible paint is the correctly-sized one.
	if width <= 0 || height <= 0 {
		return
	}

	if !p.started {
		p.enter()
	}
	if frame == p.lastFrame && !p.needsClear {
		return
	}

	newLines := strings.Split(frame, "\n")
	// We can't scroll the cursor into the terminal's scrollback, so if the
	// frame is taller than the screen keep only the bottom-most lines.
	clamped := false
	if len(newLines) > height {
		newLines = newLines[len(newLines)-height:]
		clamped = true
	}

	var buf bytes.Buffer
	if p.needsClear {
		buf.WriteString(ansi.EraseEntireScreen)
		p.linesRendered = 0
		p.lastLines = nil
		p.needsClear = false
	} else if !clamped && scrollBottom > 1 {
		// Hardware scroll fast path: if the message region is the previous frame
		// shifted up by n lines, let the terminal scroll it in place so the diff
		// below only repaints the n revealed lines. We shift p.lastLines to match
		// what is now on screen, then fall through to the normal positional diff.
		if n := detectScrollUp(p.lastLines, newLines, scrollBottom); n > 0 {
			buf.WriteString(ansi.SetTopBottomMargins(1, scrollBottom))
			buf.WriteString(ansi.SU(n))
			buf.WriteString(ansi.SetTopBottomMargins(1, height)) // restore full-screen region
			shiftLinesUp(p.lastLines, n, scrollBottom)
		}
	}
	buf.WriteString(ansi.CursorHomePosition)

	for i := 0; i < len(newLines); i++ {
		canSkip := len(p.lastLines) > i && p.lastLines[i] == newLines[i]
		if canSkip {
			// Unchanged: just advance to the next line (unless it's the last).
			if i < len(newLines)-1 {
				buf.WriteByte('\n')
			}
			continue
		}

		line := newLines[i]
		if width > 0 {
			line = ansi.Truncate(line, width, "")
		}
		if width > 0 && ansi.StringWidth(line) < width {
			// Clear any leftover content from a longer previous line.
			line += ansi.EraseLineRight
		}
		buf.WriteString(line)
		if i < len(newLines)-1 {
			buf.WriteString("\r\n")
		}
	}

	// Erase any lines left over from a taller previous frame.
	if p.linesRendered > len(newLines) {
		buf.WriteString(ansi.EraseScreenBelow)
	}
	p.linesRendered = len(newLines)

	// Park the cursor at the start of the last line for consistent behavior
	// across terminals (mirrors bubbletea's alt-screen flush).
	buf.WriteString(ansi.CursorPosition(0, len(newLines)))

	// Wrap the whole update in synchronized output (DEC 2026) so the terminal
	// buffers it and presents the frame atomically — no tearing/flicker while
	// scrolling or on slow links. Terminals without support ignore the markers.
	var out bytes.Buffer
	out.WriteString(ansi.SetMode(ansi.ModeSynchronizedOutput))
	out.Write(buf.Bytes())
	out.WriteString(ansi.ResetMode(ansi.ModeSynchronizedOutput))
	_, _ = p.out.Write(out.Bytes())

	p.lastFrame = frame
	p.lastLines = newLines
}

// detectScrollUp reports the number of lines n (>0) by which region [0, bottom)
// of old was scrolled up to produce new (old[i+n] == new[i] for every preserved
// row), or 0 if new isn't a clean upward shift of old. Smaller shifts are tried
// first since a streaming log usually grows one line at a time.
func detectScrollUp(old, next []string, bottom int) int {
	if bottom <= 1 || len(old) < bottom || len(next) < bottom {
		return 0
	}
	// A region that didn't change has nothing to scroll; bail early so a static
	// or all-blank region doesn't spuriously match a shift.
	same := true
	for i := 0; i < bottom; i++ {
		if old[i] != next[i] {
			same = false
			break
		}
	}
	if same {
		return 0
	}
	for n := 1; n < bottom; n++ {
		match := true
		for i := 0; i+n < bottom; i++ {
			if old[i+n] != next[i] {
				match = false
				break
			}
		}
		if match {
			return n
		}
	}
	return 0
}

// shiftLinesUp mutates lines in place to reflect a terminal scroll-up by n
// within region [0, bottom): each row copies the row n below it and the freed
// rows at the bottom of the region are cleared (the scroll leaves them blank),
// so the caller's positional diff repaints only the genuinely new content there.
func shiftLinesUp(lines []string, n, bottom int) {
	if n <= 0 {
		return
	}
	if bottom > len(lines) {
		bottom = len(lines)
	}
	for i := 0; i+n < bottom; i++ {
		lines[i] = lines[i+n]
	}
	for i := bottom - n; i < bottom; i++ {
		if i >= 0 {
			lines[i] = ""
		}
	}
}
