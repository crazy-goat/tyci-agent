package display

// Interaction with the TUI while an agent turn is in flight: slash commands
// that belong to the interface rather than the conversation, per-tool timings,
// and opening a running tool's output.

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// typeAndEnter types a line and presses Enter through the real key path.
func typeAndEnter(m TuiModel, line string) TuiModel {
	m.input.SetValue(line)
	next, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(TuiModel)
}

// TestModelCommandOpensThePickerWhileBusy is the reported bug: while the agent
// was thinking, the busy key handler fell straight through to submit(), so
// "/model" was queued and later delivered to the MODEL as a prompt. The picker
// never opened, and the model was asked to interpret a command meant for the
// interface.
func TestModelCommandOpensThePickerWhileBusy(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.reading = false // an agent turn is in flight
	m.queue = make(chan string, 4)

	m = typeAndEnter(m, "/model")

	if !m.pickerActive {
		t.Fatal("the picker did not open")
	}
	if len(m.queueItems) != 0 {
		t.Fatalf("the command was queued as a prompt: %v", m.queueItems)
	}
	select {
	case queued := <-m.queue:
		t.Fatalf("the command reached the agent's message queue: %q", queued)
	default:
	}
	if m.input.Value() != "" {
		t.Errorf("the input should be cleared, got %q", m.input.Value())
	}
}

// TestModelCommandStillOpensThePickerWhenIdle guards the path that already
// worked, so the shared helper cannot fix one and break the other.
func TestModelCommandStillOpensThePickerWhenIdle(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.reading = true

	m = typeAndEnter(m, "/model")

	if !m.pickerActive {
		t.Fatal("the picker did not open when idle")
	}
	if len(m.blocks) != 0 {
		t.Fatalf("the command was submitted as a prompt: %+v", m.blocks)
	}
}

// TestPickingAModelWhileBusyReachesTheAgentLoop: opening the picker is only
// half of it — the choice has to leave the TUI, or the model never changes.
func TestPickingAModelWhileBusyReachesTheAgentLoop(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.reading = false
	changes := make(chan string, 4)
	m.modelChanges = changes

	m = typeAndEnter(m, "/model")
	m.pickerCursor = 2
	want := m.pickerSelectedModel()
	if want == "" {
		t.Fatal("setup: nothing highlighted")
	}

	next, _ := m.updatePicker(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(TuiModel)

	select {
	case got := <-changes:
		if got != want {
			t.Fatalf("sent %q, highlighted %q", got, want)
		}
	default:
		t.Fatal("the choice never left the TUI, so the model would not change")
	}
	if m.pickerActive {
		t.Error("the picker should close after a choice")
	}
	if m.modelName != want {
		t.Errorf("the status bar still shows %q", m.modelName)
	}
}

// TestOtherSlashCommandsAreNotQueuedAsPromptsWhileBusy: /new, /resume, /btw
// and /exit own session and history state, so the TUI does not run them
// itself. What it must never do is queue them as prompts — that handed the
// model a command meant for the interface. /btw is started at the next safe
// point via the command channel; the rest are refused with a reason.
func TestOtherSlashCommandsAreNotQueuedAsPromptsWhileBusy(t *testing.T) {
	for _, cmd := range []string{"/new", "/resume", "/btw why", "/exit"} {
		m := newPickerTestModel(testProviders, nil, "")
		m.reading = false
		m.queue = make(chan string, 4)
		m.commands = make(chan string, 4)

		m = typeAndEnter(m, cmd)

		if m.pickerActive {
			t.Errorf("%q should not open the model picker", cmd)
		}
		select {
		case got := <-m.queue:
			t.Errorf("%q was queued as a prompt (%q) — the model was asked to interpret it", cmd, got)
		default:
		}
	}
}

// TestToolEndUsesTheReportedDuration: the display used to time each row from
// the block's own start, which for a batch is the whole batch's wall-clock —
// four tools all showing 4.29s. The dispatcher's figure has to win.
func TestToolEndUsesTheReportedDuration(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "todo"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	time.Sleep(20 * time.Millisecond) // both blocks are now "old"

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok", duration: 3 * time.Millisecond})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok", duration: 4290 * time.Millisecond})

	if got := m.blocks[0].duration; got != 3*time.Millisecond {
		t.Errorf("first tool shows %v, want 3ms", got)
	}
	if got := m.blocks[1].duration; got != 4290*time.Millisecond {
		t.Errorf("second tool shows %v, want 4.29s", got)
	}
}

// TestToolEndFallsBackToItsOwnClock keeps a display that is not told a
// duration working: a single tool call is still timed, just less precisely.
func TestToolEndFallsBackToItsOwnClock(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	time.Sleep(15 * time.Millisecond)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok"})

	if m.blocks[0].duration <= 0 {
		t.Fatal("with no reported duration the block should time itself")
	}
}

// TestClickOpensTheModalForARunningTool is the reported bug: only subagents
// and finished tools opened. Clicking a bash command while it ran — the moment
// you most want to see its output — did nothing.
func TestClickOpensTheModalForARunningTool(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "compiling…"})

	if m.blocks[0].toolState != "running" {
		t.Fatalf("setup: state is %q", m.blocks[0].toolState)
	}

	m.openToolModalAt(0)

	if !m.subagentModalActive {
		t.Fatal("the modal did not open for a running tool")
	}
	if m.subagentModalBlockIdx != 0 {
		t.Errorf("the modal points at block %d", m.subagentModalBlockIdx)
	}
	if m.subagentModalDone {
		t.Error("a running tool must not be shown as finished")
	}
}

// TestModalForARunningToolShowsLiveOutput: opening it is only useful if what
// arrives while it is open lands in the block it is showing.
func TestModalForARunningToolShowsLiveOutput(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.openToolModalAt(0)

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "step two"})

	if !strings.Contains(m.blocks[0].output, "step two") {
		t.Fatalf("live output did not reach the block: %q", m.blocks[0].output)
	}

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "done", duration: time.Second})
	if !m.subagentModalDone {
		t.Error("the modal should mark itself finished when the tool ends")
	}
	if !m.subagentModalActive {
		t.Error("the modal should stay open so the result can be read")
	}
}

// TestRunningToolAdvertisesTheClick: a feature nobody can discover is not a
// feature.
func TestRunningToolAdvertisesTheClick(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})

	rendered := m.renderToolBlock(0, m.blocks[0])
	if !strings.Contains(rendered, "click") {
		t.Fatalf("a running tool does not mention the click: %q", rendered)
	}
}
