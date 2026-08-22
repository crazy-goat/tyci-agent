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
	for _, typed := range []string{"/new", "/exit", "/resume", "/resume 2"} {
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

// TestAnswerWhileBusyIsDeliveredImmediately guards review finding 6: "/answer"
// typed while a turn is in flight used to be routed through m.commands, the
// same channel /btw uses — safe, but only drained from NextMessages, i.e.
// once the CURRENT tool call and model turn finish. For a long bash, a plain
// wait(), or a sync subagent unrelated to the very job someone is trying to
// unblock, that could be minutes away. handleLocalSlashCommand must now call
// answerFunc directly and synchronously instead, with nothing going through
// the commands channel at all.
func TestAnswerWhileBusyIsDeliveredImmediately(t *testing.T) {
	m := busyModel(t, "/answer left") // reading=false: a turn is in flight
	var gotArg string
	called := false
	m.answerFunc = func(arg string) (string, bool) {
		gotArg = arg
		called = true
		return `answered job #5 (asked: "which way?") with: "left"`, true
	}

	handled, next := m.handleLocalSlashCommand()
	if !handled {
		t.Fatal("/answer fell through to submit() while busy, so the model was asked to interpret it")
	}
	if !called {
		t.Fatal("answerFunc was not called synchronously — /answer must not wait for the commands channel to be drained")
	}
	if gotArg != "left" {
		t.Fatalf("answerFunc got arg %q, want %q", gotArg, "left")
	}

	got := next.(TuiModel)
	select {
	case cmd := <-got.commands:
		t.Fatalf("/answer must not be routed through the commands channel (delays it until the current turn's tool/model call finishes), got %q", cmd)
	default:
	}
	if !strings.Contains(got.statusMessage, "answered job #5") {
		t.Fatalf("expected the confirmation (echoing the job/question) in the status message, got %q", got.statusMessage)
	}
}

// TestAnswerWhileIdleIsDeliveredSynchronously guards review finding 1: the
// wedge. Answering while idle used to be NOT intercepted here at all (this
// function returned handled=false), so it fell through to submit(), which
// flips reading to false — and only a "done"/"reset" block flips it back,
// which this command never produces. The status bar then kept claiming a
// turn was still running, with everything typed next silently piling up in
// the pending-message queue. The fix must intercept "/answer" directly and
// leave m.reading untouched.
func TestAnswerWhileIdleIsDeliveredSynchronously(t *testing.T) {
	m := busyModel(t, "/answer left")
	m.reading = true // idle: no turn in flight
	var gotArg string
	m.answerFunc = func(arg string) (string, bool) {
		gotArg = arg
		return `answered job #5 (asked: "which way?") with: "left"`, true
	}

	handled, next := m.handleLocalSlashCommand()
	if !handled {
		t.Fatal("/answer fell through to submit() while idle — this is the wedge: reading would flip to false with nothing to flip it back")
	}
	got := next.(TuiModel)
	if !got.reading {
		t.Fatal("reading was flipped to false: this is exactly the reported wedge")
	}
	if gotArg != "left" {
		t.Fatalf("answerFunc got arg %q, want %q", gotArg, "left")
	}
	if got.input.Value() != "" {
		t.Fatalf("input should be cleared after the command is taken, got %q", got.input.Value())
	}
}

// TestAnswerWithNoAnswerFuncIsRefusedNotPanicked covers a TuiModel built
// without NewTUI's onAnswer wiring (every test that constructs one via
// newModel directly, and any future caller that forgets to wire it) — it
// must be refused with a status message, not nil-call panic.
func TestAnswerWithNoAnswerFuncIsRefusedNotPanicked(t *testing.T) {
	m := busyModel(t, "/answer left")
	m.answerFunc = nil
	handled, next := m.handleLocalSlashCommand()
	if !handled {
		t.Fatal("/answer must still be intercepted even with no answerFunc wired")
	}
	got := next.(TuiModel)
	if !strings.Contains(got.statusMessage, "unavailable") {
		t.Fatalf("expected a clear refusal, got %q", got.statusMessage)
	}
}
