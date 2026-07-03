package display

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithFPS(tuiFPS())}
	if tuiMouseEnabled() {
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
		if _, err := p.Run(); err != nil {
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

// flushLoop flushes accumulated streaming content on demand. It sleeps until
// signaled via flushWake (set by Thinking/Text when content is appended), then
// waits a short coalescing window so multiple rapid appends batch into a single
// render. This keeps the loop idle (zero wakeups) when nothing is streaming.
// 33ms matches the 30 FPS renderer — flushing faster than the renderer can
// paint only burns CPU re-wrapping the streaming block.
func (t *TUI) flushLoop() {
	const coalesce = 33 * time.Millisecond

	for {
		select {
		case <-t.flushWake:
			// Coalesce: wait briefly so bursts of appends flush as one message.
			select {
			case <-time.After(coalesce):
			case <-t.done:
				t.flushPending()
				close(t.flushDone)
				return
			}
			t.flushPending()
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
