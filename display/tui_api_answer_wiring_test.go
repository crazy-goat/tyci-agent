package display

import (
	"strings"
	"testing"
)

// TestRequireAnswerWiring_PanicsWhenNil and its non-nil counterpart pin the
// exact guard NewTUI relies on (requireAnswerWiring, tui_api.go) directly,
// without paying for NewTUI's other side effects (raw terminal mode, a real
// tea.Program goroutine that reads os.Stdin and never returns on its own —
// not something a unit test should start).
//
// This guards the item-27 round-3 "composition root can silently pass nil"
// bloker: before this fix, NewTUI accepted a nil onAnswer with no
// complaint — the TUI would still start, "/answer" would still look handled
// (tui_keys.go intercepts the string synchronously), but every answer
// would be silently discarded and a blocked child would sit until its own
// timeout threw its work away, with no diagnosable failure anywhere.
func TestRequireAnswerWiring_PanicsWhenNil(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected requireAnswerWiring(nil) to panic, it returned normally")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected a string panic value, got %T: %v", r, r)
		}
		if !strings.Contains(msg, "onAnswer") || !strings.Contains(msg, "nil") {
			t.Fatalf("panic message should name the problem (onAnswer/nil), got %q", msg)
		}
	}()

	requireAnswerWiring(nil)
	t.Fatal("unreachable: requireAnswerWiring(nil) should have panicked")
}

func TestRequireAnswerWiring_DoesNotPanicWhenSet(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("requireAnswerWiring with a non-nil onAnswer must not panic, got %v", r)
		}
	}()
	requireAnswerWiring(func(arg string) (string, bool) { return "ok", true })
}

// TestNewTUI_PanicsWhenOnAnswerIsNil exercises the guard through the actual
// public entry point too — requireAnswerWiring is NewTUI's very first
// statement (see tui_api.go), so the panic fires before any terminal setup
// or goroutine is started, and this test needs no TTY and leaves nothing
// running afterward.
func TestNewTUI_PanicsWhenOnAnswerIsNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected NewTUI(..., nil) to panic, it returned normally")
		}
	}()

	NewTUI("test/model", "", nil, nil, nil, nil, "", nil, 0, 0, 0, nil)
	t.Fatal("unreachable: NewTUI should have panicked before returning")
}
