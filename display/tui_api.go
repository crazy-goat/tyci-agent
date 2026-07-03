package display

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

type TUI struct {
	prog         *tea.Program
	results      chan string
	modelChanges chan string
	cancel       chan struct{} // sent on when ESC pressed during agent run
	done         chan struct{}

	// Streaming coalescing
	mu             sync.Mutex
	pendingKind    string // "thinking" or "text"
	pendingContent strings.Builder
	flushWake      chan struct{} // signaled when pending content is appended
	flushDone      chan struct{}
}

func NewTUI(modelName string, historyPath string, models []string, allProviders []ProviderModels) *TUI {
	results := make(chan string, 8)
	modelChanges := make(chan string, 8)
	cancel := make(chan struct{}, 1)
	m := newModel(results, modelName, historyPath, models, modelChanges, allProviders, cancel)

	var opts []tea.ProgramOption
	if tuiPainterEnabled() {
		// Own the terminal ourselves via a custom event-driven painter: no
		// idle ticker and instant key echo. bubbletea's nil renderer no-ops
		// all terminal control, so the painter handles alt-screen/mouse/cursor.
		m.painter = newPainter(os.Stdout, tuiMouseEnabled())
		opts = append(opts, tea.WithoutRenderer())
	} else {
		opts = append(opts, tea.WithAltScreen(), tea.WithFPS(tuiFPS()))
	}
	if tuiMouseEnabled() {
		// Needed even in painter mode so bubbletea's input reader delivers
		// mouse events; the enable escape itself is written by the painter.
		opts = append(opts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(m, opts...)

	t := &TUI{
		prog:         p,
		results:      results,
		modelChanges: modelChanges,
		cancel:       cancel,
		done:         make(chan struct{}),
		flushWake:    make(chan struct{}, 1),
		flushDone:    make(chan struct{}),
	}

	go t.flushLoop()
	go func() {
		// bubbletea skips all terminal init when the renderer is disabled
		// (WithoutRenderer), so in painter mode we do it ourselves: raw mode,
		// initial window size, and resize forwarding. Must happen before Run().
		var restoreTerm func()
		if m.painter != nil {
			restoreTerm = setupPainterTerminal(p)
		}
		_, err := p.Run()
		// p.Run() returns after bubbletea's own shutdown (including recovered
		// panics), so this is the right place to restore the terminal state the
		// painter set up — the nil renderer never does it.
		if m.painter != nil {
			m.painter.stop()
		}
		// Release the scrollback cache file (old rendered history paged to disk).
		m.scrollback.close()
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

// tuiFPS returns the renderer framerate. bubbletea's standard renderer wakes
// up at this rate even when idle (each tick snapshots and compares the frame
// buffer), so the FPS directly sets the idle CPU floor. 30 is a good default;
// low-end boxes can lower it via TYCI_TUI_FPS at the cost of up to 1/fps of
// extra input-echo latency.
func tuiFPS() int {
	if v := os.Getenv("TYCI_TUI_FPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 5 && n <= 60 {
			return n
		}
	}
	return 30
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

// tuiPainterEnabled reports whether to use the custom event-driven painter
// (tui_painter.go) instead of bubbletea's ticker-based renderer. It is on by
// default — the painter eliminates idle ticker wakeups (0% idle CPU), removes
// key-echo latency, and scrolls the transcript in hardware. Set TYCI_TUI_PAINTER
// to 0/false/off/no to fall back to bubbletea's standard renderer.
func tuiPainterEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("TYCI_TUI_PAINTER")))
	return !(v == "0" || v == "false" || v == "off" || v == "no")
}

// Coalescing windows for flushLoop. The first chunk after a quiet period
// flushes fast so the response appears promptly; once the stream is clearly
// sustained (previous flush was recent), batching harder cuts the number of
// transcript repaints 3x with no visible difference at reading speed.
const (
	coalesceCold  = 33 * time.Millisecond
	coalesceHot   = 100 * time.Millisecond
	coalesceHotIf = 300 * time.Millisecond // a flush this recent means the stream is hot
)

// nextCoalesce picks the coalescing window given the time since the last flush.
func nextCoalesce(sinceLastFlush time.Duration) time.Duration {
	if sinceLastFlush < coalesceHotIf {
		return coalesceHot
	}
	return coalesceCold
}

// flushLoop flushes accumulated streaming content on demand. It sleeps until
// signaled via flushWake (set by Thinking/Text when content is appended), then
// waits a coalescing window so multiple rapid appends batch into a single
// render. This keeps the loop idle (zero wakeups) when nothing is streaming.
func (t *TUI) flushLoop() {
	var lastFlush time.Time

	for {
		select {
		case <-t.flushWake:
			// Coalesce: wait briefly so bursts of appends flush as one message.
			select {
			case <-time.After(nextCoalesce(time.Since(lastFlush))):
			case <-t.done:
				t.flushPending()
				close(t.flushDone)
				return
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
	t.mu.Lock()
	kind := t.pendingKind
	content := t.pendingContent.String()
	t.pendingKind = ""
	t.pendingContent.Reset()
	t.mu.Unlock()

	if content != "" && kind != "" {
		t.post(tuiMsgBlock{kind: kind, content: content})
	}
}

// flushNow forces an immediate flush of pending content.
func (t *TUI) flushNow() {
	t.mu.Lock()
	kind := t.pendingKind
	content := t.pendingContent.String()
	t.pendingKind = ""
	t.pendingContent.Reset()
	t.mu.Unlock()

	if content != "" && kind != "" {
		t.post(tuiMsgBlock{kind: kind, content: content})
	}
}

// ModelChanges returns a channel that receives new model names when the user
// switches model via Tab/Shift+Tab.
