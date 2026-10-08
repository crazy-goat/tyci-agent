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

// TestModelCommandIsSentLikeAnyUnknownSlashCommand: the TUI has no /model.
// The line takes the same path as any other slash command the TUI does not
// know: it is queued while the agent is busy and submitted while idle.
func TestModelCommandIsSentLikeAnyUnknownSlashCommand(t *testing.T) {
	for _, line := range []string{"/model", "/no-such-command"} {
		busy := newTestModel()
		busy.reading = false
		busy.queue = make(chan string, 4)

		busy = typeAndEnter(busy, line)

		select {
		case got := <-busy.queue:
			if got != line {
				t.Errorf("queued %q, want %q", got, line)
			}
		default:
			t.Errorf("%q was not queued while the agent was busy", line)
		}

		idle := newTestModel()
		idle.reading = true

		idle = typeAndEnter(idle, line)

		if len(idle.blocks) == 0 || idle.blocks[len(idle.blocks)-1].content != "You: "+line {
			t.Errorf("%q was not submitted as a prompt while idle: %+v", line, idle.blocks)
		}
	}
}

// TestOtherSlashCommandsAreNotQueuedAsPromptsWhileBusy: /new, /resume, /btw
// and /exit own session and history state, so the TUI does not run them
// itself. What it must never do is queue them as prompts — that handed the
// model a command meant for the interface. /btw is started at the next safe
// point via the command channel; the rest are refused with a reason.
func TestOtherSlashCommandsAreNotQueuedAsPromptsWhileBusy(t *testing.T) {
	for _, cmd := range []string{"/new", "/resume", "/btw why", "/exit"} {
		m := newTestModel()
		m.reading = false
		m.queue = make(chan string, 4)
		m.commands = make(chan string, 4)

		m = typeAndEnter(m, cmd)

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
	m := newTestModel()

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
	m := newTestModel()

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
	m := newTestModel()
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
	m := newTestModel()
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
	m := newTestModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})

	rendered := m.renderToolBlock(0, m.blocks[0])
	if !strings.Contains(rendered, "click") {
		t.Fatalf("a running tool does not mention the click: %q", rendered)
	}
}
