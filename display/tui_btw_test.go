package display

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func newBtwTestModel() TuiModel {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 100
	m.height = 40
	return m
}

// ─── entry registration / streaming ──────────────────────────────────────

func TestUpdateBtwMsg_OpenRegistersEntryAndOpensModal(t *testing.T) {
	m := newBtwTestModel()

	updated, _ := m.updateBtwMsg(tuiBtwOpenMsg{id: "btw-1", question: "what time is it?", createdAt: time.Now()})
	tm := updated.(TuiModel)

	if len(tm.btwEntries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(tm.btwEntries))
	}
	if tm.btwEntries[0].ID != "btw-1" || tm.btwEntries[0].Question != "what time is it?" {
		t.Errorf("unexpected entry: %+v", tm.btwEntries[0])
	}
	if !tm.btwModalActive {
		t.Error("expected the modal to open immediately on /btw")
	}
	if tm.btwModalEntry != tm.btwEntries[0] {
		t.Error("expected the modal to show the entry that was just opened")
	}
}

func TestUpdateBtwMsg_JobIDIsRecordedOnMatchingEntry(t *testing.T) {
	m := newBtwTestModel()
	updated, _ := m.updateBtwMsg(tuiBtwOpenMsg{id: "btw-1", question: "q", createdAt: time.Now()})
	m = updated.(TuiModel)

	updated, _ = m.updateBtwMsg(tuiBtwJobIDMsg{id: "btw-1", jobID: "job-42"})
	m = updated.(TuiModel)

	if m.btwEntries[0].JobID != "job-42" {
		t.Errorf("expected JobID to be recorded, got %q", m.btwEntries[0].JobID)
	}
}

func TestUpdateBtwMsg_StreamAccumulatesIntoMatchingEntryOnly(t *testing.T) {
	m := newBtwTestModel()
	updated, _ := m.updateBtwMsg(tuiBtwOpenMsg{id: "btw-1", question: "q1", createdAt: time.Now()})
	m = updated.(TuiModel)
	updated, _ = m.updateBtwMsg(tuiBtwOpenMsg{id: "btw-2", question: "q2", createdAt: time.Now()})
	m = updated.(TuiModel)

	updated, _ = m.updateBtwMsg(tuiBtwStreamMsg{id: "btw-1", kind: "text", content: "hello "})
	m = updated.(TuiModel)
	updated, _ = m.updateBtwMsg(tuiBtwStreamMsg{id: "btw-1", kind: "text", content: "world"})
	m = updated.(TuiModel)

	e1 := m.findBtwEntry("btw-1")
	e2 := m.findBtwEntry("btw-2")
	if e1.content.String() != "hello world" {
		t.Errorf("expected accumulated content %q, got %q", "hello world", e1.content.String())
	}
	if e2.content.String() != "" {
		t.Errorf("stream for btw-1 must not leak into btw-2, got %q", e2.content.String())
	}
}

func TestUpdateBtwMsg_StreamIgnoredForUnknownEntry(t *testing.T) {
	m := newBtwTestModel()
	// Should not panic even though no entry with this id exists.
	_, _ = m.updateBtwMsg(tuiBtwStreamMsg{id: "no-such-entry", kind: "text", content: "hi"})
}

func TestUpdateBtwMsg_DoneMarksEntryFinished(t *testing.T) {
	m := newBtwTestModel()
	updated, _ := m.updateBtwMsg(tuiBtwOpenMsg{id: "btw-1", question: "q", createdAt: time.Now()})
	m = updated.(TuiModel)

	updated, _ = m.updateBtwMsg(tuiBtwStreamMsg{id: "btw-1", kind: "done", content: ""})
	m = updated.(TuiModel)

	if !m.btwEntries[0].done {
		t.Error("expected entry to be marked done")
	}
	if m.btwEntries[0].errMsg != "" {
		t.Errorf("expected no error, got %q", m.btwEntries[0].errMsg)
	}
}

func TestUpdateBtwMsg_DoneWithErrorRecordsMessage(t *testing.T) {
	m := newBtwTestModel()
	updated, _ := m.updateBtwMsg(tuiBtwOpenMsg{id: "btw-1", question: "q", createdAt: time.Now()})
	m = updated.(TuiModel)

	updated, _ = m.updateBtwMsg(tuiBtwStreamMsg{id: "btw-1", kind: "done", content: "boom"})
	m = updated.(TuiModel)

	if !m.btwEntries[0].done {
		t.Error("expected entry to be marked done even on failure")
	}
	if m.btwEntries[0].errMsg != "boom" {
		t.Errorf("expected error %q, got %q", "boom", m.btwEntries[0].errMsg)
	}
}

// TestUpdateBtwMsg_StreamNeverDroppedWhileAnotherModalIsActive is the
// requirement this dispatch order exists for: a background /btw job's
// output must keep accumulating even if the user has some other popup open
// (here, the model picker) — Update dispatches btw messages before any
// exclusivity check.
func TestUpdateBtwMsg_StreamNeverDroppedWhileAnotherModalIsActive(t *testing.T) {
	m := newBtwTestModel()
	updated, _ := m.updateBtwMsg(tuiBtwOpenMsg{id: "btw-1", question: "q", createdAt: time.Now()})
	m = updated.(TuiModel)
	m.closeBtwModal()
	m.pickerActive = true // simulate the user opening /model while btw runs

	newM, _ := m.Update(tuiBtwStreamMsg{id: "btw-1", kind: "text", content: "still streaming"})
	tm := newM.(TuiModel)

	if !tm.pickerActive {
		t.Error("the other popup should stay open — btw streaming must not steal focus")
	}
	if tm.findBtwEntry("btw-1").content.String() != "still streaming" {
		t.Error("btw content should keep accumulating while another modal is active")
	}
}

// ─── live modal keys ──────────────────────────────────────────────────────

func TestUpdateBtwModal_EscapeClosesEvenWhileRunning(t *testing.T) {
	m := newBtwTestModel()
	updated, _ := m.updateBtwMsg(tuiBtwOpenMsg{id: "btw-1", question: "q", createdAt: time.Now()})
	m = updated.(TuiModel)

	newM, _ := m.updateBtwModal(tea.KeyMsg{Type: tea.KeyEscape})
	tm := newM.(TuiModel)

	if tm.btwModalActive {
		t.Error("ESC should close the /btw modal even while the job is still running")
	}
}

func TestUpdateBtwModal_EnterOnlyClosesWhenDone(t *testing.T) {
	m := newBtwTestModel()
	updated, _ := m.updateBtwMsg(tuiBtwOpenMsg{id: "btw-1", question: "q", createdAt: time.Now()})
	m = updated.(TuiModel)

	newM, _ := m.updateBtwModal(tea.KeyMsg{Type: tea.KeyEnter})
	tm := newM.(TuiModel)
	if !tm.btwModalActive {
		t.Error("Enter should NOT close the modal while running")
	}

	tm.btwEntries[0].done = true
	newM, _ = tm.updateBtwModal(tea.KeyMsg{Type: tea.KeyEnter})
	tm = newM.(TuiModel)
	if tm.btwModalActive {
		t.Error("Enter should close the modal once the entry is done")
	}
}

// ─── list popup ────────────────────────────────────────────────────────

func TestBtwListEntries_NewestFirst(t *testing.T) {
	m := newBtwTestModel()
	updated, _ := m.updateBtwMsg(tuiBtwOpenMsg{id: "first", question: "q1", createdAt: time.Now()})
	m = updated.(TuiModel)
	m.closeBtwModal()
	updated, _ = m.updateBtwMsg(tuiBtwOpenMsg{id: "second", question: "q2", createdAt: time.Now()})
	m = updated.(TuiModel)

	entries := m.btwListEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].ID != "second" || entries[1].ID != "first" {
		t.Errorf("expected newest-first order, got [%s, %s]", entries[0].ID, entries[1].ID)
	}
}

func TestUpdateBtwList_EnterOpensSelectedEntry(t *testing.T) {
	m := newBtwTestModel()
	updated, _ := m.updateBtwMsg(tuiBtwOpenMsg{id: "first", question: "q1", createdAt: time.Now()})
	m = updated.(TuiModel)
	m.closeBtwModal()
	updated, _ = m.updateBtwMsg(tuiBtwOpenMsg{id: "second", question: "q2", createdAt: time.Now()})
	m = updated.(TuiModel)
	m.closeBtwModal()
	m.openBtwList()

	// Cursor 0 = newest ("second").
	newM, _ := m.updateBtwList(tea.KeyMsg{Type: tea.KeyEnter})
	tm := newM.(TuiModel)

	if tm.btwListActive {
		t.Error("Enter should close the list popup")
	}
	if !tm.btwModalActive {
		t.Fatal("Enter should open the live/preview modal for the selected entry")
	}
	if tm.btwModalEntry.ID != "second" {
		t.Errorf("expected to open the entry under the cursor (newest), got %q", tm.btwModalEntry.ID)
	}
}

func TestUpdateBtwList_EscapeClosesWithoutOpeningAnything(t *testing.T) {
	m := newBtwTestModel()
	m.openBtwList()

	newM, _ := m.updateBtwList(tea.KeyMsg{Type: tea.KeyEscape})
	tm := newM.(TuiModel)

	if tm.btwListActive {
		t.Error("ESC should close the list popup")
	}
	if tm.btwModalActive {
		t.Error("ESC should not open the live modal")
	}
}

func TestUpdateBtwList_EnterOnEmptyListIsNoop(t *testing.T) {
	m := newBtwTestModel()
	m.openBtwList()

	newM, _ := m.updateBtwList(tea.KeyMsg{Type: tea.KeyEnter})
	tm := newM.(TuiModel)

	if !tm.btwListActive {
		t.Error("Enter on an empty list should be a no-op, not close the popup")
	}
	if tm.btwModalActive {
		t.Error("Enter on an empty list must not open a modal")
	}
}

// ─── rendering smoke tests (no panics, non-empty output) ─────────────────

func TestRenderBtwModalView_DoesNotPanic(t *testing.T) {
	m := newBtwTestModel()
	updated, _ := m.updateBtwMsg(tuiBtwOpenMsg{id: "btw-1", question: "q", createdAt: time.Now()})
	m = updated.(TuiModel)
	updated, _ = m.updateBtwMsg(tuiBtwStreamMsg{id: "btw-1", kind: "text", content: "some output\nacross lines\n"})
	m = updated.(TuiModel)

	out := m.renderBtwModalView()
	if out == "" {
		t.Error("expected non-empty rendered modal view")
	}
}

func TestRenderBtwListView_DoesNotPanicWhenEmpty(t *testing.T) {
	m := newBtwTestModel()
	m.openBtwList()
	out := m.renderBtwListView()
	if out == "" {
		t.Error("expected non-empty rendered list view even with no entries")
	}
}
