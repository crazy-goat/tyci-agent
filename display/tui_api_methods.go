package display

import (
	"context"
	"fmt"
	"strings"

	"github.com/decodo/tyci/stream"
)

func (t *TUI) ModelChanges() <-chan string {
	return t.modelChanges
}

// SetModel updates the model name displayed in the status bar.
func (t *TUI) SetModel(name string) {
	t.prog.Send(tuiMsgBlock{kind: "set-model", content: name})
}

// Results returns the channel that receives submitted lines from the TUI.
func (t *TUI) Results() <-chan string {
	return t.results
}

// DoneCh returns a channel that is closed when the TUI program exits.
func (t *TUI) DoneCh() <-chan struct{} {
	return t.done
}

// CancelCh returns a channel that is sent on when the user presses ESC during
// an agent run, requesting cancellation of the current operation.
func (t *TUI) CancelCh() <-chan struct{} {
	return t.cancel
}

func (t *TUI) post(msg tuiMsgBlock) { t.prog.Send(msg) }

// Request is a no-op in TUI mode — request boundaries are implicit
// in the rendered transcript.
func (t *TUI) Request(string) {}

// ToolFinish is a no-op in TUI mode — tool summaries are not rendered.
func (t *TUI) ToolFinish() {}

func (t *TUI) Thinking(text string) {
	t.mu.Lock()
	if t.pendingKind != "" && t.pendingKind != "thinking" {
		// Kind changed, flush previous
		kind := t.pendingKind
		content := t.pendingContent.String()
		t.pendingKind = "thinking"
		t.pendingContent.Reset()
		t.pendingContent.WriteString(text)
		t.mu.Unlock()
		if content != "" {
			t.post(tuiMsgBlock{kind: kind, content: content})
		}
	} else {
		t.pendingKind = "thinking"
		t.pendingContent.WriteString(text)
		t.mu.Unlock()
	}
}

func (t *TUI) Text(text string) {
	t.mu.Lock()
	if t.pendingKind != "" && t.pendingKind != "text" {
		// Kind changed, flush previous
		kind := t.pendingKind
		content := t.pendingContent.String()
		t.pendingKind = "text"
		t.pendingContent.Reset()
		t.pendingContent.WriteString(text)
		t.mu.Unlock()
		if content != "" {
			t.post(tuiMsgBlock{kind: kind, content: content})
		}
	} else {
		t.pendingKind = "text"
		t.pendingContent.WriteString(text)
		t.mu.Unlock()
	}
}

func (t *TUI) ToolCallStart(name string) {
	t.flushNow()
	t.post(tuiMsgBlock{kind: "tool-start", toolName: name})
}

func (t *TUI) ToolCallDelta(delta string) {
	t.flushNow()
	t.post(tuiMsgBlock{kind: "tool-delta", content: delta})
}

func (t *TUI) ToolCallEnd(name, result string) {
	t.flushNow()
	t.post(tuiMsgBlock{kind: "tool-end", content: result})
}

func (t *TUI) ToolBlock(msg string) {
	t.flushNow()
	// In TUI, "⏳ waiting for tools..." is noise; tools are rendered live via ToolCallStart/Delta/End.
	// Skip it.
	if strings.HasPrefix(msg, "⏳") {
		return
	}
	t.post(tuiMsgBlock{kind: "block", content: msg})
}

func (t *TUI) Summary(usage stream.Usage, stats stream.Stats) {
	t.flushNow()
	t.post(tuiMsgBlock{kind: "usage", usage: usage, stats: stats})
}

func (t *TUI) Error(err error) {
	t.flushNow()
	t.post(tuiMsgBlock{kind: "error", content: err.Error()})
}

func (t *TUI) End() {
	t.flushNow()
}

func (t *TUI) Done(usage stream.Usage, stats stream.Stats) {
	t.flushNow()
	t.post(tuiMsgBlock{kind: "done", usage: usage, stats: stats})
}

// ResetStatus resets the TUI state to idle/reading after an interruption.
func (t *TUI) ResetStatus() {
	t.post(tuiMsgBlock{kind: "done"})
}

func (t *TUI) Reset() {
	t.post(tuiMsgBlock{kind: "reset"})
}

// ShowTotalUsage displays accumulated total usage after a reset (/new).
// Timing stats (t=, ttft=, tok/s) are per-request and not meaningful for
// session totals, so we build the line manually without them.
func (t *TUI) ShowTotalUsage(usage stream.Usage) {
	line := BuildUsageLineNoTiming(usage)
	t.post(tuiMsgBlock{kind: "block", content: "───── new conversation ─────"})
	t.post(tuiMsgBlock{kind: "block", content: "📊 Session total: " + line})
}

// StreamProgress sends incremental tool output to the TUI.
// toolIdx is the index of the tool in the current tool batch (0-based).
func (t *TUI) StreamProgress(toolIdx int, line string) {
	t.post(tuiMsgBlock{kind: "tool-progress", toolIdx: toolIdx, content: line + "\n"})
}

func (t *TUI) ReadInput(_ context.Context, _ string) (string, error) {
	select {
	case line, ok := <-t.results:
		if !ok {
			return "", context.Canceled
		}
		return line, nil
	case <-t.done:
		return "", fmt.Errorf("TUI closed")
	}
}

func (t *TUI) Wait()  { <-t.done }
func (t *TUI) Close() { t.prog.Quit() }
