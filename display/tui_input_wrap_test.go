package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// The textarea lays soft-wrapped rows out through its private wrap(); bubbles
// v1.0.0 LineCount() only counts hard newlines and cannot see them, which is
// why capInputHeight used to keep a long single-line prompt one row tall.
//
// These tests pin inputWrappedRows()/wrapRunes (the verbatim copy of that
// wrap) to the widget's own geometry via LineInfo().Height, which is exactly
// len(memoizedWrap(value[row], m.width)) for the cursor's logical line —
// straight from the same data the renderer uses, no rendering involved.
//
// Width trap: textarea.SetWidth(W) subtracts the prompt and line-number
// reservation before storing m.width, so the wrap width is ta.Width(), not
// the W passed to SetWidth.

// wrapTestCases covers the shapes the prompt actually sees: long runs without
// spaces (URLs), prose with spaces, CJK/double-width runes, trailing spaces,
// and the width edges. Each entry is a single logical line.
func wrapTestCases() []struct {
	name string
	w    int
	text string
} {
	return []struct {
		name string
		w    int
		text string
	}{
		{"long_no_spaces_200_at_38", 38, strings.Repeat("a", 200)},
		{"long_prose_at_38", 38, strings.Repeat("word ", 60)},
		{"cjk_wide_runes", 38, strings.Repeat("你好世界", 20)},
		{"emoji_width_runes", 30, strings.Repeat("a🙂b", 15)},
		{"trailing_spaces", 38, strings.Repeat("ab ", 30)},
		{"exact_width_chars", 38, strings.Repeat("a", 38)},
		{"width_minus_one", 38, strings.Repeat("a", 37)},
		{"width_plus_one", 38, strings.Repeat("a", 39)},
		{"short_single_word", 38, "hello"},
		{"double_width_crossing_edge", 10, strings.Repeat("你", 6)},
	}
}

// newWrapOracle returns a textarea configured like the production prompt
// (display/tui.go: line numbers off) and reports the number of wrapped rows
// the widget itself lays out for a single logical line.
func newWrapOracle(t *testing.T, w int, line string) int {
	t.Helper()
	ta := textarea.New()
	ta.ShowLineNumbers = false // as in newModel; default prompt "> " kept
	ta.SetWidth(w)
	ta.Focus()
	ta.SetValue(line) // cursor lands on the last logical line
	return ta.LineInfo().Height
}

// TestWrapRunes_MatchesTextareaGeometry is the regression test: wrapRunes must
// produce exactly as many rows as the textarea lays out for the same text at
// its effective wrap width.
func TestWrapRunes_MatchesTextareaGeometry(t *testing.T) {
	for _, tc := range wrapTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			widgetRows := newWrapOracle(t, tc.w, tc.text)
			helperRows := len(wrapRunes([]rune(tc.text), tc.w-2)) // -2: default "> " prompt reservation
			if helperRows != widgetRows {
				t.Fatalf("wrapRunes=%d rows, textarea lays out %d rows (w=%d, %d chars)",
					helperRows, widgetRows, tc.w, len([]rune(tc.text)))
			}
		})
	}
}

// TestCapInputHeight_LongSingleLinePromptGrows drives capInputHeight through
// prompts that used to stay at height 1 under LineCount(): after the fix the
// height must match the widget's own wrapped layout, clamped to [1,10].
func TestCapInputHeight_LongSingleLinePromptGrows(t *testing.T) {
	for _, tc := range []struct {
		name string
		w    int
		text string
	}{
		{"no_spaces_200_cols", 40, strings.Repeat("a", 200)},
		{"prose_300_cols", 40, strings.Repeat("word ", 60)},
		{"cjk_160_cols", 40, strings.Repeat("你好世界", 20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &TuiModel{}
			m.input = textarea.New()
			m.input.ShowLineNumbers = false
			m.input.SetWidth(tc.w)
			m.input.SetValue(tc.text)

			want := newWrapOracle(t, tc.w, tc.text)
			if want < 2 {
				t.Fatalf("precondition: case must span several wrapped rows, got %d", want)
			}
			if want > 10 {
				want = 10
			}

			m.capInputHeight()
			if m.input.Height() != want {
				t.Fatalf("height=%d, want %d (widget's wrapped layout); LineCount() would have kept this at 1",
					m.input.Height(), want)
			}
		})
	}
}

// TestCapInputHeight_Boundaries pins the edges: empty stays at one row, and a
// value far beyond ten wrapped rows clamps at ten.
func TestCapInputHeight_Boundaries(t *testing.T) {
	m := &TuiModel{}
	m.input = textarea.New()
	m.input.ShowLineNumbers = false
	m.input.SetWidth(38)

	m.capInputHeight()
	if m.input.Height() != 1 {
		t.Fatalf("empty input: height=%d, want 1", m.input.Height())
	}

	m.input.SetValue(strings.Repeat("word ", 400) + "\n" + strings.Repeat("x", 400))
	m.capInputHeight()
	if m.input.Height() != 10 {
		t.Fatalf("huge input: height=%d, want clamp at 10", m.input.Height())
	}
}

// TestInputWrappedRows_SumsHardNewlines checks the multi-line half against the
// per-line oracle sum: the helper must split on hard newlines and sum each
// line's wrap, matching what the widget lays out for the whole value.
func TestInputWrappedRows_SumsHardNewlines(t *testing.T) {
	const w = 38
	lines := []string{
		strings.Repeat("word ", 30),
		strings.Repeat("b", 100),
		"short",
		strings.Repeat("你好世界", 8),
	}

	want := 0
	for _, l := range lines {
		want += newWrapOracle(t, w, l)
	}

	m := &TuiModel{}
	m.input = textarea.New()
	m.input.ShowLineNumbers = false
	m.input.SetWidth(w)
	m.input.SetValue(strings.Join(lines, "\n"))

	if got := m.inputWrappedRows(); got != want {
		t.Fatalf("inputWrappedRows()=%d, want %d (sum of per-line widget layout)", got, want)
	}
	if lc := m.input.LineCount(); lc >= want {
		t.Fatalf("precondition broken: LineCount=%d not below wrapped rows %d", lc, want)
	}
}

func newlineKeyCases() []struct {
	name string
	msg  tea.KeyMsg
} {
	return []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"alt_enter", tea.KeyMsg{Type: tea.KeyEnter, Alt: true}},
		{"ctrl_n", tea.KeyMsg{Type: tea.KeyCtrlN}},
		{"ctrl_j", tea.KeyMsg{Type: tea.KeyCtrlJ}},
	}
}

// TestNewlineKeys_PreSetHeightUsesWrappedRows drives the idle Alt+Enter /
// Ctrl+N / Ctrl+J paths end-to-end: the pre-set height (+1, then the
// capInputHeight re-cap) must come from the wrapped row count, so a long
// soft-wrapped prompt keeps its full multi-row height across the newline
// instead of collapsing to LineCount()+1 = 2.
func TestNewlineKeys_PreSetHeightUsesWrappedRows(t *testing.T) {
	for _, c := range newlineKeyCases() {
		t.Run(c.name, func(t *testing.T) {
			m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
			m.ready = true
			m.width = 100
			m.height = 30
			m.reading = true
			m.input.SetWidth(80)                          // production default; inner width 78
			m.input.SetValue(strings.Repeat("word ", 60)) // ~5+ wrapped rows, 1 logical line

			model, _ := m.Update(c.msg)
			m2 := model.(TuiModel)

			wrapped := 0
			for _, l := range strings.Split(m2.input.Value(), "\n") {
				wrapped += newWrapOracle(t, 80, l)
			}
			if wrapped < 2 {
				t.Fatalf("precondition: value must span multiple wrapped rows, got %d", wrapped)
			}
			if h := m2.input.Height(); h != minMaxClamp(wrapped, 1, 10) {
				t.Fatalf("after newline height=%d, want %d (wrapped rows clamped)", h, minMaxClamp(wrapped, 1, 10))
			}
			if m2.input.LineCount() != 2 {
				t.Fatalf("expected exactly one inserted newline, LineCount=%d", m2.input.LineCount())
			}
		})
	}
}

// TestNewlineKeys_BusyPathPreSetHeightUsesWrappedRows covers the same three
// keys in the busy handler, whose pre-set blocks mirror the idle ones.
func TestNewlineKeys_BusyPathPreSetHeightUsesWrappedRows(t *testing.T) {
	for _, c := range newlineKeyCases() {
		t.Run(c.name, func(t *testing.T) {
			m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
			m.ready = true
			m.width = 100
			m.height = 30
			m.reading = false // busy path
			m.input.SetWidth(80)
			m.input.SetValue(strings.Repeat("word ", 60))

			model, _ := m.Update(c.msg)
			m2 := model.(TuiModel)

			wrapped := 0
			for _, l := range strings.Split(m2.input.Value(), "\n") {
				wrapped += newWrapOracle(t, 80, l)
			}
			if wrapped < 3 {
				t.Fatalf("precondition: value must span several wrapped rows, got %d", wrapped)
			}
			// LineCount()+1 would give 2 here regardless of wrapping; the
			// wrapped-row pre-set must land on minMaxClamp(wrapped, 1, 10)
			// after capInputHeight's re-cap.
			if h := m2.input.Height(); h != minMaxClamp(wrapped, 1, 10) {
				t.Fatalf("busy path: after newline height=%d, want %d (wrapped rows clamped; LineCount()+1 would be 2)", h, minMaxClamp(wrapped, 1, 10))
			}
		})
	}
}

func minMaxClamp(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}
