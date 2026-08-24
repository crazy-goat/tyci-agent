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

// SetSessionLister wires the Sidebar's Sessions tab (TODO item 1) to fn,
// which should enumerate this project's resumable sessions on demand — the
// same call bare "/resume" makes (session.ResumeEntries, wrapped by main()).
// Called once from main(), outside the bubbletea event-loop goroutine, so it
// goes through the same message-send pattern as every other cross-goroutine
// mutation here rather than writing the model directly.
func (t *TUI) SetSessionLister(fn func() []TuiResumeEntry) {
	t.prog.Send(tuiSetSessionListerMsg{fn: fn})
}

// SetModel updates the model name displayed in the status bar.
func (t *TUI) SetModel(name string) {
	t.prog.Send(tuiMsgBlock{kind: "set-model", content: name})
}

// Results returns the channel that receives submitted lines from the TUI.
func (t *TUI) Results() <-chan string {
	return t.results
}

// Commands returns the channel that receives main-loop slash commands typed
// while a turn is in flight (the main loop is blocked in the agent run then,
// so it cannot read Results). The caller services them at a safe point — in
// practice from the same NextMessages hook that drains queued user lines, so
// the conversation is not being written to while a command reads it.
func (t *TUI) Commands() <-chan string {
	return t.commands
}

// DrainCommands takes every command queued so far, oldest first, and leaves
// the channel empty. Returns nil when there is nothing to do.
func (t *TUI) DrainCommands() []string {
	var out []string
	for {
		select {
		case c := <-t.commands:
			out = append(out, c)
		default:
			return out
		}
	}
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

// HasPendingMessages reports whether a line is waiting to be read.
//
// Wired to tools.SetUserPending so a tool that can move its work to the
// background does so the moment someone types, instead of making them wait out
// the rest of a 30- or 60-second window. Reading the channel's length is
// enough: nothing here needs to know what was typed, only that somebody is
// waiting.
func (t *TUI) HasPendingMessages() bool {
	return len(t.queue) > 0
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

// ToolCallDuration reports how long the tool whose ToolCallEnd comes next
// actually took. It satisfies the agent's optional toolDurationSink.
//
// The display cannot measure this itself: every ToolCallStart in a batch is
// emitted before the batch runs and every ToolCallEnd after it finishes, so
// timing from start to end shows the whole batch on every row. Stored on the
// sender side and attached to the next tool-end message, which is safe because
// both calls come from the dispatcher's own goroutine, in that order.
func (t *TUI) ToolCallDuration(d time.Duration) {
	t.pendingToolDuration = d
}

func (t *TUI) ToolCallEnd(name, result string) {
	t.flushNow()
	d := t.pendingToolDuration
	t.pendingToolDuration = 0
	t.post(tuiMsgBlock{
		kind:     "tool-end",
		content:  result,
		duration: d,
		failed:   toolCallResultFailed(name, result),
	})
}

// toolCallResultFailed recognises the result conventions shared by the tool
// dispatcher and the built-in tools. ToolCallEnd deliberately keeps its public
// two-argument signature; the private flag on tuiMsgBlock carries this extra
// display-only state without widening every Display implementation.
func toolCallResultFailed(name, result string) bool {
	result = strings.TrimSpace(result)
	if strings.HasPrefix(result, "Error:") {
		return true
	}
	// BashTool formats a non-zero process exit as this marker. Normally the
	// dispatcher prefixes it with "Error:", but recognising the native form
	// keeps direct display users and tests correct too.
	if name == "bash" && strings.HasPrefix(result, "❌ exit code ") {
		return true
	}
	// Lua's ToolResult.Error is normally wrapped by the dispatcher as
	// "Error: ...". Keep the native failure forms recognisable for callers that
	// feed a tool result directly to the display.
	if name == "lua" && (strings.HasPrefix(result, "lua error:") ||
		strings.HasPrefix(result, "lua script timed out") ||
		strings.HasPrefix(result, "lua script was cancelled")) {
		return true
	}
	return false
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

// ResetJobs clears the visible background-job history and ignores late events
// for old IDs. New IDs remain accepted after the reset.
func (t *TUI) ResetJobs(jobIDs []string) {
	t.prog.Send(tuiMsgJobsReset{jobIDs: append([]string(nil), jobIDs...)})
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
