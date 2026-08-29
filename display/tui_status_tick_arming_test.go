package display

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Item 56: statusTickCmd() used to be started only from the idle Enter path
// (tui_keys.go:66) and self-chain only while !m.reading. Every other way a
// turn can start — the busy-path Enter, the sidebar's /resume, and turns the
// REPL starts itself from a job notice without ever calling submit() — never
// armed it, so the elapsed counter froze. These tests pin the fix: a single
// armStatusTick() helper, gated on !reading && !statusTickArmed, called from
// every one of those places.

// ─── dead chain revived by request-start ───────────────────────────────

func TestStatusTick_DeadChainRearmedByRequestStart(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 100
	m.height = 40
	m.ready = true
	// Simulate a turn that ended (reading flipped back to true) while the
	// tick chain still thought it was running — the exact "dead chain"
	// state statusTickMsg's idle branch is supposed to clean up.
	m.reading = true
	m.statusTickArmed = true

	result, cmd := m.Update(statusTickMsg{})
	if cmd != nil {
		t.Fatalf("statusTickMsg while idle should return nil cmd (stop ticking), got %v", cmd)
	}
	m2 := result.(TuiModel)
	if m2.statusTickArmed {
		t.Fatal("statusTickMsg should clear statusTickArmed once the chain stops")
	}

	// Now a turn starts without ever calling submit() — e.g. the REPL
	// driving itself from a job notice (tui_mode.go's JobNotices.Signal
	// case). request-start is the one event guaranteed to fire regardless,
	// so it must revive the dead chain with exactly one tick cmd.
	cmd2 := m2.handleBlockMsg(tuiMsgBlock{kind: "request-start"})
	if cmd2 == nil {
		t.Fatal("request-start should arm a fresh tick chain after the old one died")
	}
	if m2.reading {
		t.Error("request-start should set reading = false: a turn began without submit()")
	}
	if !m2.statusTickArmed {
		t.Error("request-start should record the chain as armed")
	}
}

// ─── no second chain while already armed ───────────────────────────────

func TestStatusTick_NoSecondChainWhileArmed(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 100
	m.height = 40
	m.ready = true
	m.reading = true

	cmd1 := m.handleBlockMsg(tuiMsgBlock{kind: "request-start"})
	if cmd1 == nil {
		t.Fatal("first request-start should arm the tick chain")
	}
	if !m.statusTickArmed {
		t.Fatal("expected statusTickArmed to be set after the first request-start")
	}

	cmd2 := m.handleBlockMsg(tuiMsgBlock{kind: "request-start"})
	if cmd2 != nil {
		t.Error("a second request-start while the chain is already armed must not start a second chain")
	}
}

// TestStatusTick_PhaseAlsoArms is the same guarantee for the "phase" kind
// (agent.PhaseSink's sending/waiting/thinking transitions), the other
// handler item 56 calls out alongside request-start.
func TestStatusTick_PhaseAlsoArms(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 100
	m.height = 40
	m.ready = true
	m.reading = false // a turn is already in flight, as it is whenever "phase" arrives

	cmd := m.handleBlockMsg(tuiMsgBlock{kind: "phase", content: "waiting"})
	if cmd == nil {
		t.Fatal("phase should arm the tick chain when it isn't armed yet")
	}
	if !m.statusTickArmed {
		t.Fatal("expected statusTickArmed to be set after phase")
	}

	cmd2 := m.handleBlockMsg(tuiMsgBlock{kind: "phase", content: "thinking"})
	if cmd2 != nil {
		t.Error("a later phase while already armed must not start a second chain")
	}
}

// ─── the busy-path Enter (tui_keys.go:254) arms the tick ───────────────

func TestHandleKeyWhileBusy_EnterArmsStatusTick(t *testing.T) {
	results := make(chan string, 1)
	m := newModel(results, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.queue = make(chan string, 4)
	cancelCh := make(chan struct{}, 1)
	m.cancelCh = cancelCh
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = false // agent busy: this Enter only queues (see submit())
	m.input.SetValue("follow-up message")
	m.input.SetHeight(1)

	_, cmd := m.handleKeyWhileBusy(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Error("Enter on the busy path should call armStatusTick, not hardcode a nil cmd")
	}
}

// ─── the sidebar's /resume submit (tui_sidebar.go:705) arms the tick ───

func TestSidebarSubmitResume_ArmsStatusTick(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	m.height = 30
	m.ready = true
	m.reading = true // idle: sidebarSubmitResume refuses otherwise

	_, cmd := m.sidebarSubmitResume()
	if cmd == nil {
		t.Error("sidebarSubmitResume should return a tick cmd once its submit() starts the /resume turn")
	}
}

// ─── the idle Enter path (tui_keys.go:66) still arms the tick ─────────

func TestHandleKeyMsg_IdleEnterArmsStatusTick(t *testing.T) {
	results := make(chan string, 1)
	m := newModel(results, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = true
	m.input.SetValue("hello")

	_, cmd := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Error("idle Enter should arm the tick chain via armStatusTick")
	}
}

// ─── token-count formatting (item 56, part 2) ──────────────────────────

func TestThroughputSuffix_ShowsTokenCountFromFirstDelta(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	// A delta just landed — too soon for the rate's 0.5s floor, but the
	// count is shown immediately (item 56: "the count ... appears from
	// the first delta").
	m.roundFirstDeltaAt = time.Now()
	m.roundBytes = 340 * 4 // 340 tok at the bytes/4 estimate

	result := m.throughputSuffix()

	if !strings.Contains(result, "~340 tok") {
		t.Errorf("expected '~340 tok', got: %q", result)
	}
	if strings.Contains(result, "tok/s") {
		t.Errorf("rate should not appear before the 0.5s floor, got: %q", result)
	}
}

func TestThroughputSuffix_ShowsCountAndRateTogether(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.roundFirstDeltaAt = time.Now().Add(-2 * time.Second)
	m.roundBytes = 800 // 200 tok over 2s = 100 tok/s

	result := m.throughputSuffix()

	if !strings.Contains(result, "~200 tok ") {
		t.Errorf("expected '~200 tok ' before the rate, got: %q", result)
	}
	if !strings.Contains(result, "~100 tok/s") {
		t.Errorf("expected '~100 tok/s', got: %q", result)
	}
}

func TestThroughputSuffix_EmptyBeforeFirstDelta(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	// roundFirstDeltaAt left zero: no delta has arrived yet.

	result := m.throughputSuffix()

	if result != "" {
		t.Errorf("expected no suffix before the first delta, got: %q", result)
	}
}

// TestBuildStatus_RespondingIncludesTokenCount is the wire-format lock from
// item 56: "⟳ responding... 4.1s · ~340 tok · ~85 tok/s".
func TestBuildStatus_RespondingIncludesTokenCount(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.reading = false
	m.status = "responding"
	m.requestStartTime = time.Now().Add(-4100 * time.Millisecond)
	m.roundFirstDeltaAt = time.Now().Add(-4 * time.Second)
	m.roundBytes = 340 * 4
	m.width = 200

	result := stripANSI(m.buildStatus())

	if !strings.Contains(result, "⟳ responding... 4.1s · ~340 tok · ") {
		t.Errorf("expected the responding line with elapsed time and token count, got: %q", result)
	}
}

// TestBuildStatus_ThinkingIncludesTokenCount is the same wire format for the
// "thinking" status, which item 56 calls out explicitly alongside
// "responding".
func TestBuildStatus_ThinkingIncludesTokenCount(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.reading = false
	m.status = "thinking"
	m.requestStartTime = time.Now().Add(-1 * time.Second)
	m.roundFirstDeltaAt = time.Now()
	m.roundBytes = 40 // too soon for the rate, count still shows
	m.width = 200

	result := stripANSI(m.buildStatus())

	if !strings.Contains(result, "⟳ thinking... 1.0s · ~10 tok") {
		t.Errorf("expected thinking line with token count, got: %q", result)
	}
	if strings.Contains(result, "tok/s") {
		t.Errorf("rate should not appear before the 0.5s floor, got: %q", result)
	}
}
