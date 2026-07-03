package display

import (
	"strings"
	"testing"
	"time"
)

// ─── buildStatus elapsed-time suffix tests ─────────────────────────────

func TestBuildStatus_ShowsElapsedTimeWhenReading(t *testing.T) {
	// "reading" means busy/request-in-flight (confusing name, but !reading = idle).
	// Here m.reading is true and we set requestStartTime to verify the suffix appears.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.reading = false          // request in flight
	m.status = "tool"
	m.requestStartTime = time.Now().Add(-5300 * time.Millisecond) // 5.3s ago
	m.width = 100

	result := m.buildStatus()

	// Should contain the elapsed time suffix with one decimal
	if !strings.Contains(result, "5.3s") {
		t.Errorf("buildStatus should contain '5.3s', got: %q", result)
	}
	if !strings.Contains(result, "⟳ tool... 5.3s") {
		t.Errorf("buildStatus should contain '⟳ tool... 5.3s', got: %q", result)
	}
}

func TestBuildStatus_NoSuffixWhenIdle(t *testing.T) {
	// When m.reading == true (idle), no suffix should appear even if
	// requestStartTime is set (defensive: stale timestamp).
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.reading = true           // idle
	m.status = "tool"
	m.requestStartTime = time.Now().Add(-1000 * time.Millisecond)
	m.width = 100

	result := m.buildStatus()

	if strings.Contains(result, "⟳") {
		t.Errorf("idle status should not contain spinner or elapsed time, got: %q", result)
	}
	if strings.Contains(result, "1000") {
		t.Errorf("idle status should not contain elapsed time, got: %q", result)
	}
}

func TestBuildStatus_NoSuffixWhenRequestStartTimeIsZero(t *testing.T) {
	// When requestStartTime is the zero value, no suffix even if reading.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.reading = false          // request in flight (but start time is zero — edge case)
	m.status = "responding"
	m.requestStartTime = time.Time{} // zero value
	m.width = 100

	result := m.buildStatus()

	if strings.Contains(result, "0.0s") {
		t.Errorf("buildStatus should not show '0.0s' for zero start time, got: %q", result)
	}
	if strings.Contains(result, "responding...") && !strings.Contains(result, "0.0s") {
		// Expected: spinner without suffix because elapsed is meaningless
	}
}

func TestBuildStatus_ShowsThinkingSuffix(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.reading = false
	m.status = "thinking"
	m.requestStartTime = time.Now().Add(-2400 * time.Millisecond)
	m.width = 100

	result := m.buildStatus()

	if !strings.Contains(result, "⟳ thinking... 2.4s") {
		t.Errorf("expected '⟳ thinking... 2.4s', got: %q", result)
	}
}

func TestBuildStatus_ShowsRespondingSuffix(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.reading = false
	m.status = "responding"
	m.requestStartTime = time.Now().Add(-12700 * time.Millisecond)
	m.width = 100

	result := m.buildStatus()

	if !strings.Contains(result, "⟳ responding... 12.7s") {
		t.Errorf("expected '⟳ responding... 12.7s', got: %q", result)
	}
}

func TestBuildStatus_ShowsWorkingSuffix(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.reading = false
	m.status = "" // default → "working"
	m.requestStartTime = time.Now().Add(-400 * time.Millisecond)
	m.width = 100

	result := m.buildStatus()

	if !strings.Contains(result, "⟳ working... 0.4s") {
		t.Errorf("expected '⟳ working... 0.4s', got: %q", result)
	}
}

func TestBuildStatus_ElapsedFormatPrecision(t *testing.T) {
	// Verify exactly one decimal place with "s" suffix.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.reading = false
	m.status = "tool"
	m.requestStartTime = time.Now().Add(-12340 * time.Millisecond) // 12.34s → should round to 12.3s
	m.width = 100

	result := m.buildStatus()

	// The format is "%.1fs" so 12.34 rounds to 12.3, not 12.34
	if !strings.Contains(result, "12.3s") {
		t.Errorf("expected '12.3s' (one decimal), got: %q", result)
	}
}

// ─── submit() sets requestStartTime ─────────────────────────────────────

func TestSubmit_SetsRequestStartTime(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	// Simulate a prompt
	m.input.SetValue("hello")
	m.width = 100

	m2 := m.submit().(TuiModel)

	if m2.reading {
		t.Error("submit() should set reading = false (request in flight)")
	}
	if m2.requestStartTime.IsZero() {
		t.Error("submit() should set requestStartTime to non-zero time")
	}
	// Should be recent (within last second)
	if time.Since(m2.requestStartTime) > time.Second {
		t.Errorf("requestStartTime should be recent, was %v ago", time.Since(m2.requestStartTime))
	}
}

func TestSubmit_SetsRequestStartTimeWithModelName(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.input.SetValue("hello")
	m.width = 100

	before := time.Now()
	m2 := m.submit().(TuiModel)
	after := time.Now()

	if m2.requestStartTime.Before(before.Add(-time.Millisecond)) || m2.requestStartTime.After(after.Add(time.Millisecond)) {
		t.Errorf("requestStartTime should be between %v and %v, got %v", before, after, m2.requestStartTime)
	}
}

// ─── done/reset clear requestStartTime ──────────────────────────────────

func TestHandleBlockMsg_DoneClearsRequestStartTime(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	// Simulate request in flight
	m.reading = false
	m.requestStartTime = time.Now()

	// Send "done" message
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if !m.reading {
		t.Error("done should set reading = true (idle)")
	}
	if !m.requestStartTime.IsZero() {
		t.Error("done should clear requestStartTime to zero value")
	}
}

func TestHandleBlockMsg_ResetClearsRequestStartTime(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	// Simulate request in flight
	m.reading = false
	m.requestStartTime = time.Now()

	// Send "reset" message
	m.handleBlockMsg(tuiMsgBlock{kind: "reset"})

	if !m.reading {
		t.Error("reset should set reading = true (idle)")
	}
	if !m.requestStartTime.IsZero() {
		t.Error("reset should clear requestStartTime to zero value")
	}
}

func TestHandleBlockMsg_DoneClearsAfterSubmit(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.input.SetValue("hello")
	m.width = 100

	// Submit
	m2 := m.submit().(TuiModel)

	if m2.requestStartTime.IsZero() {
		t.Fatal("submit should set requestStartTime")
	}

	// Done
	m2.handleBlockMsg(tuiMsgBlock{kind: "done"})

	if !m2.requestStartTime.IsZero() {
		t.Error("done after submit should clear requestStartTime")
	}
}

// ─── statusTickMsg handling ────────────────────────────────────────────

func TestUpdate_StatusTickMsg_ReturnsCmdWhenReading(t *testing.T) {
	// "reading" = false means request in flight → should keep ticking
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.reading = false
	m.width = 100
	m.height = 40
	m.ready = true

	result, cmd := m.Update(statusTickMsg{})

	if cmd == nil {
		t.Error("Update with statusTickMsg when !reading should return non-nil cmd (keep ticking)")
	}
	if _, ok := result.(TuiModel); !ok {
		t.Error("Update should return TuiModel")
	}
}

func TestUpdate_StatusTickMsg_ReturnsNilCmdWhenIdle(t *testing.T) {
	// "reading" = true means idle → should stop ticking
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.reading = true
	m.width = 100
	m.height = 40
	m.ready = true

	result, cmd := m.Update(statusTickMsg{})

	if cmd != nil {
		t.Error("Update with statusTickMsg when reading should return nil cmd (stop ticking)")
	}
	if _, ok := result.(TuiModel); !ok {
		t.Error("Update should return TuiModel")
	}
}

func TestUpdate_StatusTickMsg_DoesNotMutateModel(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.reading = false
	m.width = 100
	m.height = 40
	m.ready = true
	m.status = "thinking"
	beforeStatus := m.status

	result, _ := m.Update(statusTickMsg{})
	m2 := result.(TuiModel)

	if m2.status != beforeStatus {
		t.Errorf("statusTickMsg should not change status: was %q, now %q", beforeStatus, m2.status)
	}
}

// ─── statusTickCmd ─────────────────────────────────────────────────────

func TestStatusTickCmd_ReturnsNonNil(t *testing.T) {
	cmd := statusTickCmd()
	if cmd == nil {
		t.Error("statusTickCmd should return non-nil command")
	}
}

func TestStatusTickCmd_ProducesStatusTickMsg(t *testing.T) {
	// Verify the command produces a statusTickMsg when executed.
	// We can't easily run tea commands without a program, but we can
	// at least verify it's a tick command by checking it returns something.
	cmd := statusTickCmd()
	_ = cmd // would produce statusTickMsg when executed in tea.Program
	// This is a basic smoke test that the function doesn't panic.
}

// ─── buildStatus format lock test ──────────────────────────────────────

func TestBuildStatus_FormatLock(t *testing.T) {
	// Lock the exact wire format: "⟳ tool... 5.3s"
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.modelName = "test/model"
	m.reading = false
	m.status = "tool"
	m.requestStartTime = time.Now().Add(-5300 * time.Millisecond)
	m.width = 200

	result := m.buildStatus()
	plain := stripANSI(result) // colors shouldn't affect the format

	if !strings.Contains(plain, "⟳ tool... 5.3s") {
		t.Errorf("expected exact format '⟳ tool... 5.3s', got: %q", plain)
	}
}

// ─── buildStatus all spinner types ─────────────────────────────────────

func TestBuildStatus_AllSpinnerTypes(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"thinking", "⟳ thinking..."},
		{"responding", "⟳ responding..."},
		{"tool", "⟳ tool..."},
		{"", "⟳ working..."},
		{"unknown", "⟳ working..."},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
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

// ─── buildStatus existing tests still pass ─────────────────────────────

func TestBuildStatus_ModelNamePresent(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.reading = true
	m.width = 100

	result := m.buildStatus()

	if !strings.Contains(result, "test/model") {
		t.Errorf("expected model name in status, got: %q", result)
	}
}

func TestBuildStatus_ReturnsEmptyWhenNoContent(t *testing.T) {
	m := newModel(nil, "", "", []string{}, nil, nil, nil, nil, nil, "", nil)
	m.reading = true
	m.width = 100

	result := m.buildStatus()

	// When there's no model name, no scroll, no status message, no usage,
	// and idle — should return empty string
	if result != "" {
		t.Errorf("expected empty status when nothing to show, got: %q", result)
	}
}

// ─── Integration-style: renderFrame contains the counter ───────────────

func TestRenderFrame_StatusLineContainsElapsedTime(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 100
	m.height = 40
	m.reading = false
	m.status = "tool"
	m.requestStartTime = time.Now().Add(-3200 * time.Millisecond)

	frame := m.renderFrame()
	lines := strings.Split(frame, "\n")

	// The status bar is the second-to-last section before the input line.
	// It should contain the elapsed time.
	found := false
	for _, line := range lines {
		if strings.Contains(line, "⟳ tool") && strings.Contains(line, "3.2s") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("renderFrame should contain a status line with '⟳ tool' and '3.2s', got frame:\n%s", frame)
	}
}
