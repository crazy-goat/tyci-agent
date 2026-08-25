package display

import (
	"strings"
	"testing"
)

// busyModel is a model in the state the bug lived in: a turn is in flight, so
// the main loop is not reading and Enter would otherwise queue the line as a
// prompt.
func busyModel(t *testing.T, typed string) TuiModel {
	t.Helper()
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.reading = false
	m.commands = make(chan string, 4)
	m.input.SetValue(typed)
	return m
}

// TestBtwWhileBusyGoesToTheCommandChannel is the reported bug: "/btw" typed
// while the agent was working was delivered to the model as a prompt instead
// of opening a side conversation.
func TestBtwWhileBusyGoesToTheCommandChannel(t *testing.T) {
	for _, typed := range []string{"/btw", "/btw why is this slow?"} {
		m := busyModel(t, typed)
		handled, next := m.handleLocalSlashCommand()
		if !handled {
			t.Fatalf("%q fell through to submit(), so the model was asked to interpret it", typed)
		}
		got := next.(TuiModel)
		select {
		case cmd := <-got.commands:
			if cmd != typed {
				t.Fatalf("command channel got %q, want %q", cmd, typed)
			}
		default:
			t.Fatalf("%q was swallowed: nothing reached the command channel", typed)
		}
		if got.input.Value() != "" {
			t.Fatalf("input should be cleared after the command is taken, got %q", got.input.Value())
		}
	}
}

// TestConversationChangingCommandsAreRefusedWhileBusy: these replace or end
// the conversation the running turn is writing to, so they cannot run mid-turn
// — but they must not be handed to the model either.
func TestConversationChangingCommandsAreRefusedWhileBusy(t *testing.T) {
	for _, typed := range []string{"/new", "/exit", "/resume", "/resume 2", "/compact", "/compact keep tests"} {
		m := busyModel(t, typed)
		handled, next := m.handleLocalSlashCommand()
		if !handled {
			t.Fatalf("%q fell through to submit(), so the model was asked to interpret it", typed)
		}
		got := next.(TuiModel)
		if got.statusMessage == "" {
			t.Fatalf("%q was refused with no reason shown", typed)
		}
		if !strings.Contains(got.statusMessage, "Esc") {
			t.Errorf("the refusal should say how to get what was asked for: %q", got.statusMessage)
		}
		// The typed line stays put so Esc-then-Enter does what the person meant.
		if got.input.Value() != typed {
			t.Errorf("input = %q, want the line left in place (%q)", got.input.Value(), typed)
		}
		select {
		case cmd := <-got.commands:
			t.Fatalf("%q must not be started mid-turn, but %q was queued", typed, cmd)
		default:
		}
	}
}

// TestSlashCommandsAreUntouchedWhenIdle: when the main loop IS reading, these
// commands must reach it through the normal results channel, exactly as before.
func TestSlashCommandsAreUntouchedWhenIdle(t *testing.T) {
	for _, typed := range []string{"/btw hello", "/new", "/exit", "/resume"} {
		m := busyModel(t, typed)
		m.reading = true
		if handled, _ := m.handleLocalSlashCommand(); handled {
			t.Fatalf("%q was intercepted while idle; the main loop owns it", typed)
		}
	}
}

// TestPlainTextWhileBusyIsStillAPrompt guards the interception from getting
// greedy: only slash commands are diverted.
func TestPlainTextWhileBusyIsStillAPrompt(t *testing.T) {
	m := busyModel(t, "and also check the logs")
	if handled, _ := m.handleLocalSlashCommand(); handled {
		t.Fatal("an ordinary line must still be queued as a prompt")
	}
}

func TestDrainCommandsIsFIFOAndEmpties(t *testing.T) {
	tui := &TUI{commands: make(chan string, 4)}
	if got := tui.DrainCommands(); got != nil {
		t.Fatalf("expected nil from an empty channel, got %v", got)
	}
	tui.commands <- "/btw first"
	tui.commands <- "/btw second"
	got := tui.DrainCommands()
	if len(got) != 2 || got[0] != "/btw first" || got[1] != "/btw second" {
		t.Fatalf("expected FIFO order, got %v", got)
	}
	if again := tui.DrainCommands(); again != nil {
		t.Fatalf("expected the channel to be empty, got %v", again)
	}
}
