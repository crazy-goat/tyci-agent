package display

// The status line while tools are running. It used to read "⟳ tool... 13.7s",
// which answered neither question a person watching it actually has: which
// tool, and how long has THAT one been going. The 13.7s was the whole turn's
// elapsed time, so a slow tool one second in looked identical to a wedged one.

import (
	"strings"
	"testing"
	"time"
)

func TestStatusNamesTheRunningTool(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})

	got := m.runningToolsStatus(" 13.7s")
	if !strings.Contains(got, "read") {
		t.Fatalf("the status does not name the tool: %q", got)
	}
	if strings.Contains(got, "tool...") {
		t.Errorf("still the anonymous label: %q", got)
	}
}

// TestStatusNamesEveryToolInAParallelBatch: "3 tools" tells you nothing about
// which one is slow, and a parallel batch is exactly when you want to know.
func TestStatusNamesEveryToolInAParallelBatch(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	for _, name := range []string{"read", "bash", "find"} {
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: name})
	}

	got := m.runningToolsStatus(" 1.0s")
	for _, name := range []string{"read", "bash", "find"} {
		if !strings.Contains(got, name) {
			t.Errorf("%q missing from %q", name, got)
		}
	}
}

func TestStatusCapsAVeryWideBatch(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	for i := 0; i < 9; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	}

	got := m.runningToolsStatus(" 1.0s")
	if !strings.Contains(got, "more") {
		t.Fatalf("a wide batch should be summarised, not printed in full: %q", got)
	}
}

// TestStatusTimesTheToolNotTheTurn is the point of the change: the number has
// to be the age of the thing you are waiting for.
func TestStatusTimesTheToolNotTheTurn(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	// Pretend the tool started long before the turn's elapsed suffix suggests.
	m.blocks[0].startTime = time.Now().Add(-90 * time.Second)

	got := m.runningToolsStatus(" 2.0s")
	if strings.Contains(got, "2.0s") {
		t.Fatalf("the status is showing the turn's elapsed, not the tool's: %q", got)
	}
	if !strings.Contains(got, "9") { // ~90.0s
		t.Fatalf("expected the tool's own age, got %q", got)
	}
}

// TestStatusFallsBackBeforeTheFirstBlock: the status flips to "tool" a moment
// before the first tool-start block lands, and a bare "⟳" would look broken.
func TestStatusFallsBackBeforeTheFirstBlock(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	if got := m.runningToolsStatus(" 0.3s"); !strings.Contains(got, "0.3s") {
		t.Fatalf("expected the fallback, got %q", got)
	}
}

// TestStatusIgnoresFinishedTools: a finished tool stays in the transcript, and
// listing it as running would keep a stale name on screen.
func TestStatusIgnoresFinishedTools(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok", duration: time.Millisecond})

	got := m.runningToolsStatus(" 1.0s")
	if strings.Contains(got, "read") {
		t.Errorf("a finished tool is still listed as running: %q", got)
	}
	if !strings.Contains(got, "bash") {
		t.Errorf("the still-running tool is missing: %q", got)
	}
}
