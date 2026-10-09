package display

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

type TUI struct {
	prog    *tea.Program
	results chan string
	// pendingToolDuration and pendingToolFailed carry per-call status from the
	// optional agent sinks to the ToolCallEnd that immediately follows them.
	// Only ever touched from the dispatcher's goroutine.
	pendingToolDuration time.Duration
	pendingToolFailed   bool
	cancel              chan struct{} // sent on when ESC pressed during agent run
	done                chan struct{}

	// Pending-message queue: filled by the bubbletea event loop while the
	// agent is busy (see issue #88). Drained by the agent loop via
	// NextMessages() at the next safe point. Capacity 16 is well above
	// realistic follow-up count; if exceeded, additional submits drop with
	// a status message rather than blocking the event loop.
	queue chan string

	// commands carries main-loop slash commands typed while a turn is in
	// flight. Serviced at the same safe point as the pending-message queue —
	// see Commands.
	commands chan string

	// Resume picker reply channel: written to by the bubbletea event loop
	// (via closeResumePicker) with the chosen session file path on Enter,
	// or "" on Esc. Buffered with one slot, so the send never blocks the
	// event loop (see closeResumePicker). runTUI receives the value.
	resumeCh chan string

	// Streaming coalescing
	mu             sync.Mutex
	pendingKind    string // "thinking", "text", "tool-delta" or "tool-progress"
	pendingToolIdx int    // for tool-progress: index in toolQueue
	pendingContent strings.Builder
	// postMu keeps the order of posted messages: it is held from taking the
	// pending content until it is posted, so a flush by flushLoop cannot
	// overtake a message posted by the agent goroutine.
	postMu    sync.Mutex
	flushWake chan struct{} // signaled when pending content is appended
	flushDone chan struct{}
}

// NewTUI creates the TUI. sidebarVisible restores the persisted sidebar
// state: pass true to start with the right-side sidebar already open
// (production main() passes agent.GetSidebarVisible; tests pass false).
func NewTUI(modelName string, historyPath string, toolCount int, skillCount int, mcpCount int, sidebarVisible bool) *TUI {
	results := make(chan string, 8)
	cancel := make(chan struct{}, 1)
	queue := make(chan string, 16)
	resumeCh := make(chan string, 1) // one slot: the picker's send never blocks the event loop
	m := newModel(results, modelName, historyPath, cancel, toolCount, skillCount, mcpCount)
	// Restore the saved sidebar width once; a running TUI never reloads it.
	m.sidebarWidthPercent = loadSidebarWidthPercent()
	// Restore the persisted sidebar visibility (sidebar_visible in
	// ~/.tyci/config.json): the sidebar starts open when the previous session
	// closed with it open. Set directly on the fresh model — no goroutines
	// have started yet.
	if sidebarVisible {
		m.sidebarActive = true
	}
	m.queue = queue
	m.resumeCh = resumeCh
	commands := make(chan string, 8)
	m.commands = commands

	// Capture working directory and home for the top status bar.
	if dir, err := os.Getwd(); err == nil {
		m.cwd = dir
	}
	if h, err := os.UserHomeDir(); err == nil {
		m.home = h
	}

	// Own the terminal ourselves via a custom event-driven painter: no idle
	// ticker and instant key echo. bubbletea's nil renderer no-ops all terminal
	// control, so the painter handles alt-screen/mouse/cursor. See tui_painter.go.
	m.painter = newPainter(os.Stdout, tuiMouseEnabled())
	opts := []tea.ProgramOption{tea.WithoutRenderer()}
	if tuiMouseEnabled() {
		// Needed so bubbletea's input reader delivers mouse events; the enable
		// escape itself is written by the painter.
		opts = append(opts, tea.WithMouseCellMotion())
	}
	// Filter stray SGR mouse escapes that lost their leading ESC byte so
	// they don't get inserted as literal `[<NN;NN;NN[Mm]` text into the
	// textarea. Real mouse events arrive intact (with the 0x1b prefix) and
	// are parsed by bubbletea as MouseMsg as usual. See tui_input_filter.go.
	opts = append(opts, tea.WithInput(sanitizeInput(os.Stdin)))
	p := tea.NewProgram(m, opts...)

	t := &TUI{
		prog:      p,
		results:   results,
		cancel:    cancel,
		queue:     queue,
		resumeCh:  resumeCh,
		commands:  commands,
		done:      make(chan struct{}),
		flushWake: make(chan struct{}, 1),
		flushDone: make(chan struct{}),
	}

	go t.flushLoop()
	go func() {
		// bubbletea skips all terminal init when the renderer is disabled
		// (WithoutRenderer), so we do it ourselves: raw mode, initial window
		// size, and resize forwarding. Must happen before Run().
		restoreTerm := setupPainterTerminal(p)
		_, err := p.Run()
		// p.Run() returns after bubbletea's own shutdown (including recovered
		// panics), so this is the right place to restore the terminal state the
		// painter set up — the nil renderer never does it.
		m.painter.stop()
		// Release the scrollback cache file (old rendered history paged to disk).
		m.scrollback.close()
		// Close the resume-picker reply channel. If the user was sitting in
		// the popup when bubbletea exited (e.g. Ctrl+C), the outer runTUI
		// loop's select on SelectedResume() would otherwise block forever
		// waiting for a value that will never arrive.
		if resumeCh != nil {
			close(resumeCh)
		}
		if restoreTerm != nil {
			restoreTerm()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		}
		close(t.done)
	}()

	return t
}

// setupPainterTerminal performs the terminal initialization bubbletea skips
// when the renderer is disabled: it puts stdin into raw mode, sends the initial
// window size to the program, and forwards SIGWINCH as WindowSizeMsg. bubbletea
// normally does all of this in initTerminal/handleResize, but it early-returns
// for a nil renderer, leaving input cooked and the model with no real size.
// The returned func restores the terminal and must run after the program exits.
func setupPainterTerminal(p *tea.Program) func() {
	inFd := int(os.Stdin.Fd())
	var oldState *term.State
	if term.IsTerminal(inFd) {
		if st, err := term.MakeRaw(inFd); err == nil {
			oldState = st
		}
	}

	outFd := int(os.Stdout.Fd())
	sendSize := func() {
		if w, h, err := term.GetSize(outFd); err == nil && w > 0 && h > 0 {
			p.Send(tea.WindowSizeMsg{Width: w, Height: h})
		}
	}
	// Deliver the initial size once the event loop starts consuming messages.
	go sendSize()

	stopResize := watchResize(sendSize)

	return func() {
		stopResize()
		if oldState != nil {
			_ = term.Restore(inFd, oldState)
		}
	}
}

// flushLoop flushes accumulated streaming content on demand. It sleeps until
// signaled via flushWake (set by Thinking/Text when content is appended). A
// flush at most every streamFlushInterval keeps the transcript repaints to one
// per interval: the first chunk after a quiet period flushes at once, later
// chunks wait for the interval to end. This keeps the loop idle (zero wakeups)
// when nothing is streaming. Thinking, Text, ToolCallDelta and StreamProgress
// are throttled this way. A kind change, ToolCallStart, ToolCallEnd, End, Done,
// Error and the other turn events flush at once (flushNow).
func (t *TUI) flushLoop() {
	var lastFlush time.Time

	for {
		select {
		case <-t.flushWake:
			// Wait for the rest of the interval, so bursts of appends flush as one message.
			if wait := streamFlushInterval - time.Since(lastFlush); wait > 0 {
				select {
				case <-time.After(wait):
				case <-t.done:
					t.flushPending()
					close(t.flushDone)
					return
				}
			}
			t.flushPending()
			lastFlush = time.Now()
		case <-t.done:
			t.flushPending() // flush one last time
			close(t.flushDone)
			return
		}
	}
}

// wakeFlush signals the flushLoop that pending content is available. Non-blocking:
// if a wake is already pending the loop will pick it up.
func (t *TUI) wakeFlush() {
	select {
	case t.flushWake <- struct{}{}:
	default:
	}
}

// flushPending sends any accumulated streaming content as a single message.
func (t *TUI) flushPending() {
	t.postMu.Lock()
	defer t.postMu.Unlock()

	t.mu.Lock()
	msg, ok := t.takePendingLocked()
	t.mu.Unlock()

	if ok {
		t.post(msg)
	}
}

// flushNow forces an immediate flush of pending content.
func (t *TUI) flushNow() {
	t.flushPending()
}

// takePendingLocked empties the pending buffer and returns its content as a
// message. ok is false when there is nothing to send. The caller holds t.mu.
func (t *TUI) takePendingLocked() (msg tuiMsgBlock, ok bool) {
	msg = tuiMsgBlock{kind: t.pendingKind, toolIdx: t.pendingToolIdx, content: t.pendingContent.String()}
	t.pendingKind = ""
	t.pendingToolIdx = 0
	t.pendingContent.Reset()
	return msg, msg.content != "" && msg.kind != ""
}

// appendPending adds content to the pending buffer and wakes the flushLoop.
// When kind or toolIdx differs from the pending content, the pending content is
// posted first, at once. The caller must not hold t.mu or t.postMu.
func (t *TUI) appendPending(kind string, toolIdx int, content string) {
	t.postMu.Lock()
	defer t.postMu.Unlock()

	t.mu.Lock()
	var prev tuiMsgBlock
	hasPrev := false
	if t.pendingKind != "" && (t.pendingKind != kind || t.pendingToolIdx != toolIdx) {
		prev, hasPrev = t.takePendingLocked()
	}
	t.pendingKind = kind
	t.pendingToolIdx = toolIdx
	t.pendingContent.WriteString(content)
	t.mu.Unlock()

	if hasPrev {
		t.post(prev)
	}
	t.wakeFlush()
}
