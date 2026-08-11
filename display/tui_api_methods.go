package display

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/decodo/tyci/stream"
)

func (t *TUI) ModelChanges() <-chan string {
	return t.modelChanges
}

// SelectedResume returns a channel that yields the chosen session file path
// when the user presses Enter in the /resume picker. On Esc, "" is sent so the
// caller can distinguish "user dismissed" from "no picker was open". The
// channel is unbuffered and serves one picker session per openResumePicker
// call: callers must always read exactly one value (or close the TUI).
func (t *TUI) SelectedResume() <-chan string {
	return t.resumeCh
}

// OpenResumePicker sends a message to the bubbletea program to activate the
// /resume picker with the entries to display. Caller-supplied order isn't
// trusted — the picker sorts newest-first itself so a local "session list"
// walking in alphabetic order still ends up correct. Does NOT block on the
// channel; the calling goroutine owns the read on SelectedResume().
func (t *TUI) OpenResumePicker(entries []TuiResumeEntry) {
	t.prog.Send(tuiResumeRequestMsg{entries: entries})
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

// EnqueueMessage adds a user line to the pending-message queue. Returns
// false if the queue is full; callers should surface a status message in
// that case (issue #88).
func (t *TUI) EnqueueMessage(line string) bool {
	select {
	case t.queue <- line:
		return true
	default:
		return false
	}
}

// NextMessages drains the entire pending-message queue and returns the
// messages in FIFO order. Used by agent.Run as a callback to inject
// follow-up user prompts at the next safe point (issue #88).
//
// After returning the messages, posts a "queue-drained" notification to
// the bubbletea event loop carrying the drained lines. The handler
// appends a "You: …" block to the transcript for each line — so the
// user sees their queued prompt appear in the transcript at the moment
// it's actually delivered to the model, not when it was typed. Lines
// typed between the drain and the handler running are kept in the
// panel.
func (t *TUI) NextMessages() []string {
	var out []string
	for {
		select {
		case s := <-t.queue:
			out = append(out, s)
		default:
			if len(out) > 0 && t.prog != nil {
				t.prog.Send(tuiMsgBlock{kind: "queue-drained", queuedLines: out})
			}
			return out
		}
	}
}

// ClearQueue drains the pending-message queue synchronously on the calling
// goroutine. The bubbletea event loop must call this to ensure the queue is
// empty before posting ClearQueueMsg to the model.
func (t *TUI) ClearQueue() {
	for {
		select {
		case <-t.queue:
		default:
			return
		}
	}
}

func (t *TUI) post(msg tuiMsgBlock) { t.prog.Send(msg) }

// Request is called once per API turn (agent/run_once.go). It resets the
// elapsed-time counter so the status bar shows the *current* turn's wall
// time rather than accumulating from user submit through multiple tool
// loops. See issue #83.
func (t *TUI) Request(_ string) {
	t.post(tuiMsgBlock{kind: "request-start"})
}

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
	t.wakeFlush()
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
	t.wakeFlush()
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

func (t *TUI) Total(usage stream.Usage) {
	// No-op: costs are shown in the status bar at the bottom.
	// No need to also display them in the chat window.
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

// OpenBtw registers a new /btw side-conversation and opens its live modal
// immediately, before the background job has produced any output — the main
// conversation is not blocked waiting for it. Call this BEFORE starting the
// job's goroutine (see btw.go's startBtw): Send enqueues the message
// synchronously from the caller's goroutine, so Update is guaranteed to
// register the entry before any tuiBtwStreamMsg for the same id can arrive.
func (t *TUI) OpenBtw(id, question string) {
	t.prog.Send(tuiBtwOpenMsg{id: id, question: question, createdAt: time.Now()})
}

// SetBtwJobID records the background job's ID on the /btw entry once
// tools.JobRegistry.Start has returned one. Delivered as a message (not a
// direct field write) because the caller and the bubbletea event loop run on
// different goroutines.
func (t *TUI) SetBtwJobID(id, jobID string) {
	t.prog.Send(tuiBtwJobIDMsg{id: id, jobID: jobID})
}

// BtwSink returns a Sink that streams a /btw child run's output to the entry
// identified by id, live, through the same tea.Program the main conversation
// renders through. id must already have been registered via OpenBtw.
func (t *TUI) BtwSink(id string) *BtwSink {
	return NewBtwSink(t, id)
}

// OpenBtwList opens the /btw list popup (bare "/btw"), showing every entry
// recorded so far this session.
func (t *TUI) OpenBtwList() {
	t.prog.Send(tuiBtwListOpenMsg{})
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
