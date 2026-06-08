package display

import (
	"fmt"
	"os"
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
	flushTicker    *time.Ticker
	flushDone      chan struct{}
}

func NewTUI(modelName string, historyPath string, models []string, allProviders []ProviderModels) *TUI {
	results := make(chan string, 8)
	modelChanges := make(chan string, 8)
	cancel := make(chan struct{}, 1)
	m := newModel(results, modelName, historyPath, models, modelChanges, allProviders, cancel)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	t := &TUI{
		prog:         p,
		results:      results,
		modelChanges: modelChanges,
		cancel:       cancel,
		done:         make(chan struct{}),
		flushTicker:  time.NewTicker(time.Second / 30), // ~30 FPS
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

// flushLoop periodically flushes accumulated streaming content.
func (t *TUI) flushLoop() {
	for {
		select {
		case <-t.flushTicker.C:
			t.flushPending()
		case <-t.done:
			t.flushPending() // flush one last time
			t.flushTicker.Stop()
			close(t.flushDone)
			return
		}
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
