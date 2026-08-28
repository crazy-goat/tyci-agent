package display

// Item 55: the status bar used to show one bare "⟳ working..." for
// everything between Request() and the first streamed event — connecting,
// uploading the request body, the server's time-to-first-byte and the
// model's silent prefill were all indistinguishable. These tests pin the
// two new phases ("sending request", "waiting for response"), that the
// elapsed clock restarts at each phase boundary the same way "tool" already
// does, and the throughput suffix appended while thinking/responding.

import (
	"strings"
	"testing"
	"time"
)

func TestBuildStatus_ShowsSendingPhase(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.reading = false
	m.status = "sending"
	m.requestStartTime = time.Now().Add(-300 * time.Millisecond)
	m.width = 100

	result := m.buildStatus()

	if !strings.Contains(result, "⟳ sending request... 0.3s") {
		t.Errorf("expected '⟳ sending request... 0.3s', got: %q", result)
	}
}

func TestBuildStatus_ShowsWaitingPhase(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.reading = false
	m.status = "waiting"
	m.requestStartTime = time.Now().Add(-12300 * time.Millisecond)
	m.width = 100

	result := m.buildStatus()

	if !strings.Contains(result, "⟳ waiting for response... 12.3s") {
		t.Errorf("expected '⟳ waiting for response... 12.3s', got: %q", result)
	}
}

// TestHandleBlockMsg_PhaseSetsStatusAndRestartsClock is the "phase" kind's
// contract: display.TUI.Phase (agent.PhaseSink) posts this, and the label
// AND the elapsed clock must both flip at the boundary — same as the
// existing "tool" state already does with its own per-block timer.
func TestHandleBlockMsg_PhaseSetsStatusAndRestartsClock(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.requestStartTime = time.Now().Add(-30 * time.Second) // stale, from a previous phase

	m.handleBlockMsg(tuiMsgBlock{kind: "phase", content: "waiting"})

	if m.status != "waiting" {
		t.Fatalf("status = %q, want %q", m.status, "waiting")
	}
	if time.Since(m.requestStartTime) > time.Second {
		t.Errorf("requestStartTime should have been restarted at the phase boundary, was %v ago", time.Since(m.requestStartTime))
	}
}

// TestHandleBlockMsg_PhaseNoOpWhenUnchanged: a repeated Phase call for the
// SAME phase (e.g. a retried request re-emitting stream.RequestSent) must
// not rewind the clock mid-phase.
func TestHandleBlockMsg_PhaseNoOpWhenUnchanged(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.status = "waiting"
	old := time.Now().Add(-5 * time.Second)
	m.requestStartTime = old

	m.handleBlockMsg(tuiMsgBlock{kind: "phase", content: "waiting"})

	if !m.requestStartTime.Equal(old) {
		t.Errorf("requestStartTime changed on a same-phase repeat: was %v, now %v", old, m.requestStartTime)
	}
}

// TestHandleBlockMsg_RequestStartResetsThroughput: a new round must not
// inherit the previous round's byte count, or a fast first round's tail
// would inflate a slow second round's early rate.
func TestHandleBlockMsg_RequestStartResetsThroughput(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.roundBytes = 4000
	m.roundFirstDeltaAt = time.Now().Add(-10 * time.Second)

	m.handleBlockMsg(tuiMsgBlock{kind: "request-start"})

	if m.roundBytes != 0 {
		t.Errorf("roundBytes = %d, want 0 after request-start", m.roundBytes)
	}
	if !m.roundFirstDeltaAt.IsZero() {
		t.Error("roundFirstDeltaAt should be zeroed after request-start")
	}
}

// TestBuildStatus_ThroughputSuffix_AppearsAfterEnoughTime: a rate computed
// from a single delta and a few milliseconds would be wild ("~4000 tok/s"),
// so the suffix only appears once enough wall time has passed.
func TestBuildStatus_ThroughputSuffix_AppearsAfterEnoughTime(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.reading = false
	m.status = "responding"
	m.requestStartTime = time.Now().Add(-2 * time.Second)
	m.roundFirstDeltaAt = time.Now().Add(-2 * time.Second)
	m.roundBytes = 800 // 200 tokens over 2s = 100 tok/s
	m.width = 100

	result := m.buildStatus()

	if !strings.Contains(result, "tok/s") {
		t.Fatalf("expected a tok/s throughput suffix, got: %q", result)
	}
	if !strings.Contains(result, "~100 tok/s") {
		t.Errorf("expected '~100 tok/s' (800 bytes / 4 / 2s), got: %q", result)
	}
}

func TestBuildStatus_ThroughputSuffix_AbsentBeforeFirstDelta(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.reading = false
	m.status = "thinking"
	m.requestStartTime = time.Now().Add(-1 * time.Second)
	m.width = 100
	// roundFirstDeltaAt left zero: no delta has arrived yet.

	result := m.buildStatus()

	if strings.Contains(result, "tok/s") {
		t.Errorf("no delta has arrived yet, should not show a throughput figure: %q", result)
	}
}

func TestBuildStatus_ThroughputSuffix_AbsentTooSoonAfterFirstDelta(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.reading = false
	m.status = "responding"
	m.requestStartTime = time.Now()
	m.roundFirstDeltaAt = time.Now() // just now — too soon to estimate a rate
	m.roundBytes = 40
	m.width = 100

	result := m.buildStatus()

	if strings.Contains(result, "tok/s") {
		t.Errorf("less than the 0.5s floor has passed, should not show a throughput figure yet: %q", result)
	}
}

// TestHandleBlockMsg_TextTracksThroughputBytes is the wiring test: actual
// "text" kind messages (what TUI.Text posts) must feed trackDeltaBytes, not
// just a hand-set field.
func TestHandleBlockMsg_TextTracksThroughputBytes(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)

	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "hello"})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: " world"})

	if m.roundBytes != len("hello")+len(" world") {
		t.Errorf("roundBytes = %d, want %d", m.roundBytes, len("hello")+len(" world"))
	}
	if m.roundFirstDeltaAt.IsZero() {
		t.Error("roundFirstDeltaAt should be set after the first text delta")
	}
}

func TestHandleBlockMsg_ThinkingTracksThroughputBytes(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)

	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "reasoning"})

	if m.roundBytes != len("reasoning") {
		t.Errorf("roundBytes = %d, want %d", m.roundBytes, len("reasoning"))
	}
}

// TestBuildStatus_AllSpinnerTypes_IncludesNewPhases extends the existing
// lock test (tui_status_tick_test.go) to the two new phase labels.
func TestBuildStatus_AllSpinnerTypes_IncludesNewPhases(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"sending", "⟳ sending request..."},
		{"waiting", "⟳ waiting for response..."},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
			m.reading = false
			m.status = tt.status
			m.requestStartTime = time.Now().Add(-1000 * time.Millisecond)
			m.width = 200

			result := m.buildStatus()
			plain := stripANSI(result)

			if !strings.Contains(plain, tt.expected+" 1.0s") {
				t.Errorf("expected %q with 1.0s suffix, got: %q", tt.expected, plain)
			}
		})
	}
}
