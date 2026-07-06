package display

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── helpers ─────────────────────────────────────────────────────────────

// newResumePickerTestModel builds a TuiModel with the resumeCh channel
// plumbed in, mirroring the helper used by tui_picker_test.go. The model
// is sized large enough that the viewport doesn't clip rows below the
// cursor in the simple list-rendering tests.
func newResumePickerTestModel(entries []TuiResumeEntry) (TuiModel, <-chan string) {
	resumeCh := make(chan string, 1)
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 120
	m.height = 40
	m.ready = true
	m.resumeCh = resumeCh
	_ = resumeCh // silence unused if model test path doesn't read it
	return m, resumeCh
}

func mkEntries(n int) []TuiResumeEntry {
	out := make([]TuiResumeEntry, n)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		out[i] = TuiResumeEntry{
			Path:        "/tmp/sessions/session-" + strings.Repeat("x", i+1) + ".jsonl",
			Name:        "session-" + strings.Repeat("x", i+1) + ".jsonl",
			ModTime:     base.Add(time.Duration(i) * time.Hour),
			FirstPrompt: "prompt #" + strings.Repeat("p", i+1),
		}
	}
	return out
}

// runKey is a tiny helper that drives updateResumePicker and returns the
// updated model so tests don't have to type-assert the bubbletea interface
// return at every step. Mirrors the convention in tui_picker_test.go.
func runKey(m TuiModel, key tea.KeyType) TuiModel {
	out, _ := m.updateResumePicker(tea.KeyMsg{Type: key})
	return out.(TuiModel)
}

// ─── open/close lifecycle ───────────────────────────────────────────────

func TestResumePicker_Open_SetsActiveAndEntries(t *testing.T) {
	entries := mkEntries(3)
	m, _ := newResumePickerTestModel(entries)
	m.openResumePicker(entries)

	if !m.resumePickerActive {
		t.Fatal("expected resumePickerActive=true after openResumePicker")
	}
	if len(m.resumePickerEntries) != 3 {
		t.Errorf("entries len = %d, want 3", len(m.resumePickerEntries))
	}
	if m.resumePickerCursor != 0 {
		t.Errorf("cursor = %d, want 0 (newest-first start)", m.resumePickerCursor)
	}
}

func TestResumePicker_Open_SortsNewestFirst(t *testing.T) {
	// Hand the picker a list where entries[0] is the OLDEST — open
	// must re-sort before saving, so the rendered top row is newest.
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []TuiResumeEntry{
		{Path: "/old", FirstPrompt: "old", ModTime: base},
		{Path: "/new", FirstPrompt: "new", ModTime: base.Add(2 * time.Hour)},
		{Path: "/mid", FirstPrompt: "mid", ModTime: base.Add(1 * time.Hour)},
	}
	m, _ := newResumePickerTestModel(entries)
	m.openResumePicker(entries)

	if m.resumePickerEntries[0].Path != "/new" {
		t.Errorf("top entry = %q, want /new (newest-first)", m.resumePickerEntries[0].Path)
	}
	if m.resumePickerEntries[1].Path != "/mid" {
		t.Errorf("middle entry = %q, want /mid", m.resumePickerEntries[1].Path)
	}
	if m.resumePickerEntries[2].Path != "/old" {
		t.Errorf("bottom entry = %q, want /old", m.resumePickerEntries[2].Path)
	}
}

// ─── key handling ────────────────────────────────────────────────────────

func TestResumePicker_Enter_PicksAndSendsPath(t *testing.T) {
	entries := mkEntries(3)
	m, ch := newResumePickerTestModel(entries)
	m.openResumePicker(entries)

	// After open sorts newest-first: entries[2] is the top row, entries[1] the
	// middle, entries[0] the bottom. Set cursor=1 (middle) to verify Enter
	// uses the current cursor — not just the top — when picking.
	m.resumePickerCursor = 1
	// After sort newest-first, index 1 is the originally-second entry.
	wantIdx := 1
	expectedPath := m.resumePickerEntries[wantIdx].Path

	m = runKey(m, tea.KeyEnter)

	if m.resumePickerActive {
		t.Errorf("picker should close on Enter (got active=true)")
	}
	if len(m.resumePickerEntries) != 0 {
		t.Errorf("entries should clear on close, got %d", len(m.resumePickerEntries))
	}
	select {
	case got := <-ch:
		if got != expectedPath {
			t.Errorf("channel got %q, want %q", got, expectedPath)
		}
	default:
		t.Fatal("expected channel to receive path on Enter (no value)")
	}
}

// drainResumeChannel returns whatever the channel emitted (used to assert
// it's empty after Esc), but only with a 0-budget select.
func drainResumeChannel(ch <-chan string) string {
	select {
	case v := <-ch:
		return v
	default:
		return ""
	}
}

func TestResumePicker_Escape_SendsEmptyPath(t *testing.T) {
	entries := mkEntries(3)
	m, ch := newResumePickerTestModel(entries)
	m.openResumePicker(entries)

	m = runKey(m, tea.KeyEscape)

	if m.resumePickerActive {
		t.Error("picker should close on Esc")
	}
	if got := drainResumeChannel(ch); got != "" {
		t.Errorf("channel got %q on Esc, want empty string", got)
	}
}

func TestResumePicker_Navigation_UpDown(t *testing.T) {
	entries := mkEntries(5)
	m, _ := newResumePickerTestModel(entries)
	m.openResumePicker(entries)

	m = runKey(m, tea.KeyDown)
	if m.resumePickerCursor != 1 {
		t.Errorf("after Down: cursor=%d, want 1", m.resumePickerCursor)
	}
	m = runKey(m, tea.KeyDown)
	m = runKey(m, tea.KeyDown)
	if m.resumePickerCursor != 3 {
		t.Errorf("after 3 Down: cursor=%d, want 3", m.resumePickerCursor)
	}
	m = runKey(m, tea.KeyUp)
	if m.resumePickerCursor != 2 {
		t.Errorf("after Up: cursor=%d, want 2", m.resumePickerCursor)
	}
	// Clamp at top.
	m.resumePickerCursor = 0
	m = runKey(m, tea.KeyUp)
	if m.resumePickerCursor != 0 {
		t.Errorf("clamp at top: cursor=%d, want 0", m.resumePickerCursor)
	}
	// Clamp at bottom.
	m.resumePickerCursor = 4
	m = runKey(m, tea.KeyDown)
	if m.resumePickerCursor != 4 {
		t.Errorf("clamp at bottom: cursor=%d, want 4", m.resumePickerCursor)
	}
}

func TestResumePicker_Navigation_PgUpPgDnHomeEnd(t *testing.T) {
	entries := mkEntries(30) // more than 10
	m, _ := newResumePickerTestModel(entries)
	m.openResumePicker(entries)

	// From top, PgDn jumps 10.
	m = runKey(m, tea.KeyPgDown)
	if m.resumePickerCursor != 10 {
		t.Errorf("PgDn: cursor=%d, want 10", m.resumePickerCursor)
	}
	// PgDn twice more — third lands at 29 (clamp from overshoot).
	m = runKey(m, tea.KeyPgDown)
	m = runKey(m, tea.KeyPgDown)
	if m.resumePickerCursor != 29 {
		t.Errorf("PgDn clamp: cursor=%d, want 29", m.resumePickerCursor)
	}
	// Home → 0
	m = runKey(m, tea.KeyHome)
	if m.resumePickerCursor != 0 {
		t.Errorf("Home: cursor=%d, want 0", m.resumePickerCursor)
	}
	// End → last
	m = runKey(m, tea.KeyEnd)
	if m.resumePickerCursor != 29 {
		t.Errorf("End: cursor=%d, want 29", m.resumePickerCursor)
	}
	// PgUp from bottom clamps to 19.
	m = runKey(m, tea.KeyPgUp)
	if m.resumePickerCursor != 19 {
		t.Errorf("PgUp: cursor=%d, want 19", m.resumePickerCursor)
	}
	// PgUp 3× more — clamps to 0.
	for i := 0; i < 3; i++ {
		m = runKey(m, tea.KeyPgUp)
	}
	if m.resumePickerCursor != 0 {
		t.Errorf("PgUp clamp: cursor=%d, want 0", m.resumePickerCursor)
	}
}

func TestResumePicker_Enter_NoEntries_Noop(t *testing.T) {
	m, ch := newResumePickerTestModel(nil)
	m.openResumePicker(nil)

	m = runKey(m, tea.KeyEnter)

	if !m.resumePickerActive {
		t.Error("picker stays open when Enter is pressed on empty list")
	}
	if got := drainResumeChannel(ch); got != "" {
		t.Errorf("Enter on empty list sent %q, want no signal", got)
	}
}

// ─── rendering ──────────────────────────────────────────────────────────

func TestResumePicker_View_ShowsTwoColumnsAndHint(t *testing.T) {
	entries := []TuiResumeEntry{
		{
			Path:        "/tmp/s.jsonl",
			ModTime:     time.Date(2026, 1, 1, 12, 34, 56, 0, time.UTC),
			FirstPrompt: "hello world",
		},
	}
	m, _ := newResumePickerTestModel(entries)
	m.openResumePicker(entries)

	view := stripANSI(m.renderResumePickerContent())

	if !strings.Contains(view, "Resume Session") {
		t.Error("rendered popup missing title")
	}
	if !strings.Contains(view, "2026-01-01 12:34:56") {
		t.Errorf("rendered view missing date column; got:\n%s", view)
	}
	if !strings.Contains(view, "hello world") {
		t.Errorf("rendered view missing first prompt preview; got:\n%s", view)
	}
	if !strings.Contains(view, "Enter resume") {
		t.Errorf("rendered view missing the Enter-resume hint; got:\n%s", view)
	}
	if !strings.Contains(view, "Esc cancel") {
		t.Errorf("rendered view missing the Esc-cancel hint; got:\n%s", view)
	}
}

func TestResumePicker_View_EmptyListShowsEmptymessage(t *testing.T) {
	m, _ := newResumePickerTestModel(nil)
	m.openResumePicker(nil)

	view := stripANSI(m.renderResumePickerContent())
	if !strings.Contains(view, "No sessions") {
		t.Errorf("expected empty-list message; got:\n%s", view)
	}
}

func TestResumePicker_TruncatePrompt_HandlesNewlinesAndLong(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"", 10, "(no first prompt parsed)"},
		{"hi", 10, "hi"},
		{"hi", 2, ".."}, // corner: max <= 3 collapses to dots only
		{"abcdef", 3, "..."},
		{"abcdef", 5, "ab..."},
		{"line1\nline2\nline3", 40, "line1 line2 line3"}, // newline collapse
		{"line1\n\n\t line2", 40, "line1 line2"},         // whitespace collapse
	}
	for i, c := range cases {
		got := truncateResumePrompt(c.in, c.max)
		if got != c.want {
			t.Errorf("case %d: got %q, want %q", i, got, c.want)
		}
	}
}
