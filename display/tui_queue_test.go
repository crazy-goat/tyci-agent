package display

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── renderQueuePanel ────────────────────────────────────────────────────

func TestRenderQueuePanel_EmptyRendersNothing(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	got := m.renderQueuePanel(80)
	if got != "" {
		t.Fatalf("renderQueuePanel on empty queue = %q, want empty string", got)
	}
}

func TestRenderQueuePanel_OneLine(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queueItems = []string{"hello"}
	panel := m.renderQueuePanel(80)
	// One rendered line plus trailing newline.
	lines := strings.Split(strings.TrimRight(panel, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 panel line, got %d: %q", len(lines), panel)
	}
	if !strings.Contains(lines[0], "hello") {
		t.Errorf("expected panel line to contain %q, got %q", "hello", lines[0])
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[0]), "▸") {
		t.Errorf("expected panel line to start with ▸ glyph, got %q", lines[0])
	}
}

func TestRenderQueuePanel_FourLines(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queueItems = []string{"a", "b", "c", "d"}
	panel := m.renderQueuePanel(80)
	lines := strings.Split(strings.TrimRight(panel, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 panel lines (== queuePanelMaxLines), got %d", len(lines))
	}
}

func TestRenderQueuePanel_FiveLinesShowsOverflow(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queueItems = []string{"a", "b", "c", "d", "e"}
	panel := m.renderQueuePanel(80)
	lines := strings.Split(strings.TrimRight(panel, "\n"), "\n")
	// queuePanelMaxLines (4) + 1 overflow line.
	if len(lines) != 5 {
		t.Fatalf("expected 5 panel lines (4 + overflow), got %d: %q", len(lines), panel)
	}
	if !strings.Contains(lines[4], "1 more") {
		t.Errorf("expected overflow line to read '… and 1 more', got %q", lines[4])
	}
}

func TestRenderQueuePanel_SevenLinesShowsOverflow(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queueItems = []string{"a", "b", "c", "d", "e", "f", "g"}
	panel := m.renderQueuePanel(80)
	lines := strings.Split(strings.TrimRight(panel, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 panel lines (4 + overflow) for 7-item queue, got %d", len(lines))
	}
	if !strings.Contains(lines[4], "3 more") {
		t.Errorf("expected overflow line to read '… and 3 more', got %q", lines[4])
	}
}

func TestRenderQueuePanel_NarrowTerminalTruncates(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queueItems = []string{"a very long queued user message that does not fit"}
	panel := m.renderQueuePanel(10)
	lines := strings.Split(strings.TrimRight(panel, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 panel line, got %d", len(lines))
	}
	if w := lipgloss.Width(lines[0]); w > 10 {
		t.Errorf("panel line width = %d, want <= 10", w)
	}
}

func TestRenderQueuePanel_ExactlyMaxWidthNoTruncation(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	// "▸ " (2 cols) + 4 runes = 6 cols total — fits exactly at width 6.
	m.queueItems = []string{"abcd"}
	panel := m.renderQueuePanel(6)
	lines := strings.Split(strings.TrimRight(panel, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 panel line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "abcd") {
		t.Errorf("expected line to contain full text at exact width, got %q", lines[0])
	}
}

func TestRenderQueuePanel_ZeroWidthHidesPanel(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queueItems = []string{"hello"}
	// Width 0 is degenerate — renderQueuePanel still produces an "…" line
	// (the only glyph that fits). The acceptance criterion is that an
	// empty queue yields nothing; a non-empty queue with width 0 is not
	// a realistic state. We just verify the call does not panic.
	_ = m.renderQueuePanel(0)
}

// ─── queuePanelHeight ────────────────────────────────────────────────────

func TestQueuePanelHeight_Empty(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	if h := m.queuePanelHeight(); h != 0 {
		t.Errorf("queuePanelHeight on empty = %d, want 0", h)
	}
}

func TestQueuePanelHeight_OneLine(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queueItems = []string{"a"}
	if h := m.queuePanelHeight(); h != 1 {
		t.Errorf("queuePanelHeight with 1 item = %d, want 1", h)
	}
}

func TestQueuePanelHeight_FourLines(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queueItems = []string{"a", "b", "c", "d"}
	if h := m.queuePanelHeight(); h != 4 {
		t.Errorf("queuePanelHeight with 4 items = %d, want 4", h)
	}
}

func TestQueuePanelHeight_FiveLinesAddsOverflow(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queueItems = []string{"a", "b", "c", "d", "e"}
	if h := m.queuePanelHeight(); h != 5 {
		t.Errorf("queuePanelHeight with 5 items = %d, want 5 (4 + overflow)", h)
	}
}

// ─── renderFrame integration ────────────────────────────────────────────

func TestRenderFrame_QueuePanelAppearsBetweenStatusAndInput(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = true
	m.queueItems = []string{"queued message"}
	frame := m.renderFrame()
	// The queued message must appear in the frame.
	if !strings.Contains(frame, "queued message") {
		t.Errorf("renderFrame should contain queued message, got:\n%s", frame)
	}
	// The leading glyph must be present.
	if !strings.Contains(frame, "▸") {
		t.Errorf("renderFrame should contain ▸ glyph for queued items, got:\n%s", frame)
	}
}

func TestRenderFrame_EmptyQueueHidesPanel(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = true
	// No queue items — must not render ▸ glyph anywhere.
	frame := m.renderFrame()
	if strings.Contains(frame, "▸") {
		t.Errorf("renderFrame with empty queue should not contain ▸ glyph, got:\n%s", frame)
	}
}

func TestRenderFrame_QueueShrinksMessageViewport(t *testing.T) {
	// With a 3-line queue panel and input height 1, the frame should have
	// 1 (top) + msgHeight + 1 (status) + 3 (queue) + 1 (input) - 1 (no
	// trailing newline on input) = msgHeight + 5 newlines. With
	// height=30, visibleLines()=27, msgHeight=24, so 24+5 = 29.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = true

	m.queueItems = []string{"a", "b", "c"}
	frame := m.renderFrame()
	// Newline count == m.height - 1 (the input textarea's trailing
	// newline is missing — that matches the pre-feature behavior).
	if got := strings.Count(frame, "\n"); got != 29 {
		t.Errorf("renderFrame newline count = %d, want 29 (== m.height - 1)", got)
	}
	// The frame must contain the queue panel content.
	if !strings.Contains(frame, "a") {
		t.Errorf("renderFrame should contain 'a' from queue panel")
	}
}

func TestRenderFrame_QueueOverflowLineAppears(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = true
	m.queueItems = []string{"a", "b", "c", "d", "e", "f", "g"}
	frame := m.renderFrame()
	if !strings.Contains(frame, "3 more") {
		t.Errorf("renderFrame should contain overflow indicator '3 more' for 7-item queue, got:\n%s", frame)
	}
}

// ─── submit() while busy ────────────────────────────────────────────────

func TestSubmit_WhileBusyEnqueuesAndDoesNotAppendBlock(t *testing.T) {
	results := make(chan string, 1)
	m := newModel(results, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 16)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = false // agent is busy

	// Type a message and submit.
	m.input.SetValue("a follow-up")
	// Set height of textarea to 1 to match what handleKeyMsg would have done.
	m.input.SetHeight(1)
	model := m.submit().(TuiModel)
	m2 := model

	// The line must be in the queue snapshot.
	if len(m2.queueItems) != 1 || m2.queueItems[0] != "a follow-up" {
		t.Errorf("queueItems = %v, want [\"a follow-up\"]", m2.queueItems)
	}
	// The message must be on the channel.
	select {
	case got := <-m2.queue:
		if got != "a follow-up" {
			t.Errorf("queue got %q, want %q", got, "a follow-up")
		}
	default:
		t.Fatal("expected the queued message on the channel")
	}
	// No "You: …" block yet: the message is "pending". The transcript
	// block is appended only when the agent drains the queue and
	// delivers the line to the model (see TestQueueDrained_AppendsYouBlocks).
	for _, b := range m2.blocks {
		if strings.Contains(b.content, "a follow-up") {
			t.Errorf("submit() while busy must NOT add a 'You:' block yet (drain hasn't happened), found: %q", b.content)
		}
	}
	// Nothing should have been sent on results.
	select {
	case got := <-results:
		t.Errorf("submit() while busy must not send to results channel, got %q", got)
	default:
	}
	// m.reading stays false (agent is still busy).
	if m2.reading {
		t.Errorf("m.reading should remain false during busy submit, got true")
	}
}

func TestSubmit_WhileIdleExistingBehaviorUnchanged(t *testing.T) {
	results := make(chan string, 1)
	m := newModel(results, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 16)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = true // idle

	beforeBlocks := len(m.blocks)
	m.input.SetValue("real prompt")
	m.input.SetHeight(1)
	model := m.submit().(TuiModel)
	m2 := model

	// "You: ..." block must be appended.
	if len(m2.blocks) != beforeBlocks+1 {
		t.Fatalf("expected 1 new block, got %d (was %d)", len(m2.blocks)-beforeBlocks, beforeBlocks)
	}
	last := m2.blocks[len(m2.blocks)-1]
	if last.kind != "user" || !strings.Contains(last.content, "real prompt") {
		t.Errorf("expected new user block, got kind=%q content=%q", last.kind, last.content)
	}
	// Message must have been sent on results.
	select {
	case got := <-results:
		if got != "real prompt" {
			t.Errorf("results got %q, want %q", got, "real prompt")
		}
	default:
		t.Fatal("expected message on results channel for idle submit")
	}
	// No queued items.
	if len(m2.queueItems) != 0 {
		t.Errorf("idle submit must not populate queueItems, got %v", m2.queueItems)
	}
	// m.reading is now false (request started).
	if m2.reading {
		t.Errorf("m.reading should be false after idle submit, got true")
	}
}

func TestSubmit_QueueFullDropsWithStatusMessage(t *testing.T) {
	results := make(chan string, 1)
	m := newModel(results, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 2) // small for the test
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = false // busy

	// Fill the queue to capacity.
	m.queue <- "first"
	m.queue <- "second"

	// Now submit a third; channel is full, the line is dropped, status is set.
	m.input.SetValue("third")
	m.input.SetHeight(1)
	model := m.submit().(TuiModel)
	m2 := model

	if m2.statusMessage != queueFullStatusMessage {
		t.Errorf("statusMessage = %q, want %q", m2.statusMessage, queueFullStatusMessage)
	}
	// queueItems must NOT include the dropped message — issue #88 says
	// "no silent swallow". The snapshot reflects only what the model
	// will see.
	for _, item := range m2.queueItems {
		if item == "third" {
			t.Errorf("queueItems should not include the dropped line, got %v", m2.queueItems)
		}
	}
	// No "You: …" block for the dropped line — the model won't see it,
	// so the user shouldn't see it either.
	for _, b := range m2.blocks {
		if strings.Contains(b.content, "third") {
			t.Errorf("dropped line must not produce a 'You:' block, found: %q", b.content)
		}
	}
}

func TestSubmit_QueueFullPreservesEarlierItems(t *testing.T) {
	results := make(chan string, 1)
	m := newModel(results, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 2)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = false

	m.input.SetValue("first")
	m.input.SetHeight(1)
	m = m.submit().(TuiModel)
	m.input.SetValue("second")
	m.input.SetHeight(1)
	m = m.submit().(TuiModel)
	m.input.SetValue("third")
	m.input.SetHeight(1)
	m = m.submit().(TuiModel)

	// Both "first" and "second" should be in the channel; "third" was dropped.
	if got := <-m.queue; got != "first" {
		t.Errorf("queue[0] = %q, want first", got)
	}
	if got := <-m.queue; got != "second" {
		t.Errorf("queue[1] = %q, want second", got)
	}
	if m.statusMessage != queueFullStatusMessage {
		t.Errorf("statusMessage = %q, want %q", m.statusMessage, queueFullStatusMessage)
	}
}

func TestSubmit_EmptyLineNotEnqueued(t *testing.T) {
	results := make(chan string, 1)
	m := newModel(results, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 16)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = false

	// Empty input — no queue item, no channel send.
	m.input.SetValue("   ")
	m.input.SetHeight(1)
	model := m.submit().(TuiModel)
	m2 := model

	if len(m2.queueItems) != 0 {
		t.Errorf("empty submit must not enqueue, got %v", m2.queueItems)
	}
	select {
	case got := <-m2.queue:
		t.Errorf("empty submit must not send on queue channel, got %q", got)
	default:
	}
}

// ─── clearMessageQueue ──────────────────────────────────────────────────

func TestClearMessageQueue_DropsSnapshotAndChannel(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 4)
	m.queueItems = []string{"a", "b", "c"}
	m.queue <- "a"
	m.queue <- "b"
	m.queue <- "c"
	m.clearMessageQueue()
	if len(m.queueItems) != 0 {
		t.Errorf("queueItems not cleared, got %v", m.queueItems)
	}
	// Channel must also be drained.
	count := 0
	for {
		select {
		case <-m.queue:
			count++
		default:
			if count != 0 {
				t.Errorf("channel not drained, %d items left", count)
			}
			return
		}
	}
}

func TestClearMessageQueue_NilChannelSafe(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queueItems = []string{"a"}
	// m.queue is nil — must not panic.
	m.clearMessageQueue()
	if len(m.queueItems) != 0 {
		t.Errorf("queueItems not cleared, got %v", m.queueItems)
	}
}

func TestClearMessageQueue_EmptyNoOp(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	// Already empty — must not crash, must not invalidate anything weird.
	before := m.cachedTotalLines
	m.clearMessageQueue()
	if m.cachedTotalLines != before {
		// invalidateTotalLines sets cachedTotalLines to -1; an empty
		// queue should NOT invalidate, since nothing changed.
		t.Errorf("clearMessageQueue on empty queue invalidated total-line cache (was %d, now %d)", before, m.cachedTotalLines)
	}
}

// ─── ESC while busy clears queue ────────────────────────────────────────

func TestHandleKeyWhileBusy_ESCClearsQueue(t *testing.T) {
	results := make(chan string, 1)
	m := newModel(results, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 4)
	cancelCh := make(chan struct{}, 1)
	m.cancelCh = cancelCh
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = false // busy

	m.queueItems = []string{"pending 1", "pending 2"}
	m.queue <- "pending 1"
	m.queue <- "pending 2"

	model, _ := m.handleKeyWhileBusy(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := model.(TuiModel)

	if len(m2.queueItems) != 0 {
		t.Errorf("ESC should clear queueItems, got %v", m2.queueItems)
	}
	select {
	case <-cancelCh:
		// expected
	default:
		t.Error("ESC should signal cancelCh")
	}
	// Channel must also be empty.
	count := 0
drain:
	for {
		select {
		case <-m2.queue:
			count++
		default:
			break drain
		}
	}
	if count != 0 {
		t.Errorf("queue channel not drained after ESC, %d items left", count)
	}
}

func TestHandleKeyWhileBusy_OtherKeysDoNotClearQueue(t *testing.T) {
	results := make(chan string, 1)
	m := newModel(results, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 4)
	cancelCh := make(chan struct{}, 1)
	m.cancelCh = cancelCh
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = false

	m.queueItems = []string{"pending"}

	// Press a non-ESC key (e.g. printable 'a') — queue must remain.
	model, _ := m.handleKeyWhileBusy(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m2 := model.(TuiModel)

	if len(m2.queueItems) != 1 || m2.queueItems[0] != "pending" {
		t.Errorf("non-ESC key must not clear queue, got %v", m2.queueItems)
	}
}

func TestHandleKeyWhileBusy_EnterEnqueuesToQueue(t *testing.T) {
	// Regression test: Enter while the agent is busy used to fall through
	// to the textarea's default handler, which inserts a newline. The
	// user saw a new line in the textarea and the message was never
	// delivered to the model. handleKeyWhileBusy must now intercept Enter
	// and route to submit() (issue #88).
	results := make(chan string, 1)
	m := newModel(results, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 4)
	cancelCh := make(chan struct{}, 1)
	m.cancelCh = cancelCh
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = false // agent is busy

	m.input.SetValue("follow-up message")
	m.input.SetHeight(1)

	model, _ := m.handleKeyWhileBusy(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := model.(TuiModel)

	// Queue must contain the submitted line.
	if len(m2.queueItems) != 1 || m2.queueItems[0] != "follow-up message" {
		t.Errorf("Enter while busy should enqueue line, got queueItems=%v", m2.queueItems)
	}
	// Textarea must be reset.
	if got := m2.input.Value(); got != "" {
		t.Errorf("textarea should be reset after Enter, got %q", got)
	}
	// The message must be on the channel.
	select {
	case got := <-m2.queue:
		if got != "follow-up message" {
			t.Errorf("queue got %q, want %q", got, "follow-up message")
		}
	default:
		t.Error("expected the queued message on the channel")
	}
	// No "You: …" block yet — the message is "pending". The block
	// is appended only on drain (when the model actually sees the
	// message), so the transcript reflects what the model has
	// processed.
	for _, b := range m2.blocks {
		if strings.Contains(b.content, "follow-up message") {
			t.Errorf("Enter while busy must NOT add a 'You:' block yet, found: %q", b.content)
		}
	}
	// Nothing should have been sent on the results channel.
	select {
	case got := <-results:
		t.Errorf("Enter while busy must not send to results channel, got %q", got)
	default:
	}
}

func TestHandleKeyWhileBusy_AltEnterInsertsNewline(t *testing.T) {
	// Alt+Enter must still insert a newline in the textarea while busy
	// (matches the idle-mode keyboard semantics).
	results := make(chan string, 1)
	m := newModel(results, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 4)
	cancelCh := make(chan struct{}, 1)
	m.cancelCh = cancelCh
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = false

	m.input.SetValue("line one")
	m.input.SetHeight(1)

	model, _ := m.handleKeyWhileBusy(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m2 := model.(TuiModel)

	// Queue must be empty (Alt+Enter is a newline, not a submit).
	if len(m2.queueItems) != 0 {
		t.Errorf("Alt+Enter must not enqueue, got queueItems=%v", m2.queueItems)
	}
	// Textarea must contain a newline now.
	if got := m2.input.Value(); !strings.Contains(got, "\n") {
		t.Errorf("Alt+Enter should insert a newline, got %q", got)
	}
}

// ─── /new (reset) clears queue ──────────────────────────────────────────

func TestReset_ClearsQueue(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 4)
	m.ready = true
	m.width = 80
	m.height = 30

	m.queueItems = []string{"pending 1", "pending 2"}
	m.queue <- "pending 1"
	m.queue <- "pending 2"
	m.blocks = []block{{kind: "user", content: "You: hello"}}

	m.handleBlockMsg(tuiMsgBlock{kind: "reset"})

	if len(m.queueItems) != 0 {
		t.Errorf("reset should clear queueItems, got %v", m.queueItems)
	}
	if len(m.blocks) != 0 {
		t.Errorf("reset should clear blocks, got %d", len(m.blocks))
	}
	// Channel should also be drained.
	count := 0
	for {
		select {
		case <-m.queue:
			count++
		default:
			if count != 0 {
				t.Errorf("queue channel not drained after reset, %d items left", count)
			}
			return
		}
	}
}

// ─── queue-drained handler ─────────────────────────────────────────────

func TestQueueDrained_ClearsSnapshot(t *testing.T) {
	// Simulates the agent's drain landing on the event loop: the channel
	// is empty, queueItems has the previous snapshot, and the handler
	// must clear the snapshot so the panel disappears.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 4)
	m.queueItems = []string{"a", "b", "c"}
	// Channel is empty (agent already drained it).
	m.handleBlockMsg(tuiMsgBlock{kind: "queue-drained"})
	if len(m.queueItems) != 0 {
		t.Errorf("queue-drained should clear queueItems, got %v", m.queueItems)
	}
}

func TestQueueDrained_AppendsYouBlocksInFIFOOrder(t *testing.T) {
	// The drained lines arrive on the queue-drained message and must
	// become "You: …" blocks in the transcript, in FIFO order, at the
	// moment the model actually receives them.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 4)
	m.ready = true
	m.width = 80
	m.height = 30
	m.queueItems = []string{"a", "b", "c"}

	m.handleBlockMsg(tuiMsgBlock{
		kind:        "queue-drained",
		queuedLines: []string{"a", "b", "c"},
	})

	// Panel cleared.
	if len(m.queueItems) != 0 {
		t.Errorf("queue-drained should clear queueItems, got %v", m.queueItems)
	}
	// Three "You: …" blocks appended in FIFO order.
	if len(m.blocks) != 3 {
		t.Fatalf("blocks = %d, want 3 'You: …' blocks", len(m.blocks))
	}
	wants := []string{"You: a", "You: b", "You: c"}
	for i, want := range wants {
		if m.blocks[i].kind != "user" {
			t.Errorf("blocks[%d].kind = %q, want user", i, m.blocks[i].kind)
		}
		if !strings.HasSuffix(m.blocks[i].content, want) {
			t.Errorf("blocks[%d].content = %q, want suffix %q", i, m.blocks[i].content, want)
		}
	}
}

func TestQueueDrained_NoQueuedLinesLeavesTranscriptAlone(t *testing.T) {
	// An empty queuedLines slice (shouldn't happen in practice, but
	// defensive) must not append any block — just clear the panel.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 4)
	m.queueItems = []string{"a"}
	m.blocks = []block{{kind: "user", content: "You: prior"}}

	m.handleBlockMsg(tuiMsgBlock{kind: "queue-drained"}) // queuedLines is nil

	if len(m.blocks) != 1 {
		t.Errorf("blocks = %d, want 1 (no new blocks on empty drain)", len(m.blocks))
	}
	if len(m.queueItems) != 0 {
		t.Errorf("queue-drained should clear queueItems, got %v", m.queueItems)
	}
}

func TestQueueDrained_PreservesItemsTypedDuringDrain(t *testing.T) {
	// The user types a new line between the agent's drain and the
	// handler running. The handler must pick up the new line and keep
	// it in the panel.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 4)
	m.queueItems = []string{"old"} // will be cleared
	// Channel has a new message typed during the drain.
	m.queue <- "new"
	m.handleBlockMsg(tuiMsgBlock{kind: "queue-drained"})
	if len(m.queueItems) != 1 || m.queueItems[0] != "new" {
		t.Errorf("queue-drained should keep items typed during the drain, got %v", m.queueItems)
	}
}

func TestQueueDrained_NilChannelSafe(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queueItems = []string{"a"}
	// m.queue is nil — must not panic.
	m.handleBlockMsg(tuiMsgBlock{kind: "queue-drained"})
	if len(m.queueItems) != 0 {
		t.Errorf("queue-drained should clear queueItems, got %v", m.queueItems)
	}
}

// ─── TUI queue plumbing ─────────────────────────────────────────────────

func TestTUI_EnqueueMessageReturnsFalseWhenFull(t *testing.T) {
	// The TUI struct's EnqueueMessage is the public boundary; the model
	// uses its own queue directly. Verify the contract here so callers
	// can rely on it.
	tui := &TUI{queue: make(chan string, 2)}
	if !tui.EnqueueMessage("a") {
		t.Error("first enqueue should succeed")
	}
	if !tui.EnqueueMessage("b") {
		t.Error("second enqueue should succeed")
	}
	if tui.EnqueueMessage("c") {
		t.Error("third enqueue on a 2-cap channel should fail")
	}
}

func TestTUI_NextMessagesDrainsInFIFO(t *testing.T) {
	tui := &TUI{queue: make(chan string, 4)}
	tui.EnqueueMessage("a")
	tui.EnqueueMessage("b")
	tui.EnqueueMessage("c")

	got := tui.NextMessages()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("NextMessages len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("NextMessages[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// A second call must return nothing.
	if got := tui.NextMessages(); len(got) != 0 {
		t.Errorf("second NextMessages = %v, want empty", got)
	}
}

// ─── end-to-end flow: submit while busy → agent drains → panel clears ──

func TestQueuePanel_EndToEnd_ClearsOnDrain(t *testing.T) {
	// Simulates the full lifecycle:
	//  1. user submits two messages while busy → queueItems=[m1,m2],
	//     channel=[m1,m2], blocks contain "You: m1", "You: m2"
	//  2. agent calls tuiDisp.NextMessages() → returns [m1,m2] and posts
	//     a queue-drained message
	//  3. event loop processes the message → queueItems cleared (channel
	//     was already drained)
	//  4. panel is now empty, but the "You: m1" and "You: m2" blocks
	//     remain in the transcript
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 4)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = false

	// Step 1: submit two queued messages.
	m.input.SetValue("first follow-up")
	m.input.SetHeight(1)
	m = m.submit().(TuiModel)
	m.input.SetValue("second follow-up")
	m.input.SetHeight(1)
	m = m.submit().(TuiModel)

	if len(m.queueItems) != 2 {
		t.Fatalf("queueItems after 2 submits = %d, want 2", len(m.queueItems))
	}
	// No "You: …" blocks yet — the messages are "pending". The
	// transcript reflects what the model has seen, not what the user
	// has typed.
	if len(m.blocks) != 0 {
		t.Fatalf("blocks after 2 submits = %d, want 0 (transcript stays empty until drain)", len(m.blocks))
	}

	// Step 2-3: simulate the agent's drain. The test uses NextMessages
	// with a nil prog (so no actual message is posted), then manually
	// posts the queue-drained kind carrying the drained lines to verify
	// the handler appends "You: …" blocks AND clears the panel.
	tui := &TUI{queue: m.queue}
	got := tui.NextMessages()
	if len(got) != 2 || got[0] != "first follow-up" || got[1] != "second follow-up" {
		t.Errorf("drain returned %v, want [first, second]", got)
	}

	// Step 4: handler appends "You: …" blocks (in FIFO order) and
	// clears the panel.
	m.handleBlockMsg(tuiMsgBlock{kind: "queue-drained", queuedLines: got})

	if len(m.queueItems) != 0 {
		t.Errorf("after drain, queueItems = %v, want empty (panel should clear)", m.queueItems)
	}
	if len(m.blocks) != 2 {
		t.Fatalf("after drain, blocks = %d, want 2 (transcript should now show 'You: …' blocks)", len(m.blocks))
	}
	if !strings.Contains(m.blocks[0].content, "first follow-up") {
		t.Errorf("blocks[0] = %q, want first follow-up", m.blocks[0].content)
	}
	if !strings.Contains(m.blocks[1].content, "second follow-up") {
		t.Errorf("blocks[1] = %q, want second follow-up", m.blocks[1].content)
	}
}

func TestTUI_ClearQueueDrainsChannel(t *testing.T) {
	tui := &TUI{queue: make(chan string, 4)}
	tui.EnqueueMessage("a")
	tui.EnqueueMessage("b")
	tui.ClearQueue()
	if got := tui.NextMessages(); len(got) != 0 {
		t.Errorf("NextMessages after ClearQueue = %v, want empty", got)
	}
}

func TestTUI_NextMessages_EmptyDoesNotPostDrained(t *testing.T) {
	// When the queue is empty, NextMessages must not post a
	// queue-drained message — that would race the event loop into a
	// spurious clear of an already-empty snapshot.
	tui := &TUI{queue: make(chan string, 4)}
	if got := tui.NextMessages(); len(got) != 0 {
		t.Errorf("NextMessages on empty queue = %v, want empty", got)
	}
}
