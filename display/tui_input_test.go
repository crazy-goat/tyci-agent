package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// renderWidgetRows renders a fresh textarea loaded with value and returns the
// number of non-padding rows it paints. It is the oracle: it exercises the same
// ShowLineNumbers=off layout newModel uses and the widget's wrap exactly.
//
// The widget sanitizes/normalizes the incoming string (runeutil strips tabs
// and control bytes), so the authoritative content is ta.Value() — inputWrapped
// must be fed THAT, not the raw caller string, to be compared fairly.
func renderWidgetRows(t *testing.T, outerWidth int, value string) (ta textarea.Model, rows int) {
	t.Helper()
	ta = textarea.New()
	ta.ShowLineNumbers = false
	ta.SetWidth(outerWidth)
	ta.SetHeight(200)
	ta.EndOfBufferCharacter = '#'
	ta.SetValue(value)
	rows = 0
	for _, l := range strings.Split(ta.View(), "\n") {
		if !strings.Contains(l, "#") {
			rows++
		}
	}
	if rows < 1 {
		rows = 1
	}
	return ta, rows
}

// BLOCKER 1 — inputWrapped must agree with the widget's render for the SAME
// stored value and wrap width. We set the value on the widget first, then feed
// inputWrapped the widget's own Value() and Width().
func TestInputWrapped_MatchesWidgetExactly(t *testing.T) {
	inputs := []struct {
		outer int
		value string
	}{
		{14, "hello world foo bar baz"},
		{14, "hello\t world"},
		{14, "a\nb\u3000c"},
		{10, "😀😀😀 word"},
		{10, "🇵🇱🇵🇱 word"},
		{8, "超长超长超长超长 word"},
		{14, "one two\nthree four five six seven"},
		{8, "e\u0301e\u0301 word"},
		{10, "hello world "},
		{8, "aaaabbbbccccdddd"},
		{10, "a超超b word"},
		{12, ""},
		{12, "x"},
	}
	for _, c := range inputs {
		ta, rows := renderWidgetRows(t, c.outer, c.value)
		got := inputWrapped(ta.Value(), ta.Width())
		if got != rows {
			t.Fatalf("value=%q outer=%d stored=%q inner=%d: inputWrapped=%d widget=%d",
				c.value, c.outer, ta.Value(), ta.Width(), got, rows)
		}
	}
}

// BLOCKER 1 — the same oracle check, but constructing the value through the
// widget's own typing path (SetValue is the sanitizer path production uses).
func TestInputWrapped_MatchesWidget_TypedValue(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 18
	m.setInputWidth()
	typed := func(s string) {
		for _, r := range []rune(s) {
			m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	}
	typed("hello\tworld\nline two with some words and more")
	m.capInputHeight()
	// Height must equal what the widget renders for its own stored value.
	wantH := inputWrapped(m.input.Value(), m.input.Width())
	if wantH > inputMaxHeight {
		wantH = inputMaxHeight
	}
	if m.input.Height() != wantH {
		t.Fatalf("typed value: height=%d want %d (stored=%q inner=%d)",
			m.input.Height(), wantH, m.input.Value(), m.input.Width())
	}
}

// BLOCKER 2 — capInputHeight grows the field as text wraps, caps at
// inputMaxHeight, and keeps the widget as the scroll authority.
func TestCapInputHeight_GrowsKeepsCursorThenCaps(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 16
	m.setInputWidth()

	for _, r := range []rune("a b c d e f g h i j k l m n o p q r s t u v w x y z ") {
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m.capInputHeight()
	}
	rows := inputWrapped(m.input.Value(), m.input.Width())
	wantH := rows
	if wantH > inputMaxHeight {
		wantH = inputMaxHeight
	}
	if g := m.input.Height(); g != wantH {
		t.Fatalf("after typing wrapWidth=%d rows=%d: height=%d want %d",
			m.input.Width(), rows, g, wantH)
	}
	if m.input.Height() > inputMaxHeight {
		t.Fatalf("height %d exceeds cap %d", m.input.Height(), inputMaxHeight)
	}
}

// Deleting text shrinks the height back down.
func TestCapInputHeight_ShrinksWhenRowCountDrops(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 12
	m.setInputWidth()
	for _, r := range []rune(strings.Repeat("word ", 40)) {
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m.capInputHeight()
	}
	if m.input.Height() != inputMaxHeight {
		t.Fatalf("tall input height=%d want %d", m.input.Height(), inputMaxHeight)
	}
	m.input.SetValue("short")
	m.capInputHeight()
	if m.input.Height() != 1 {
		t.Fatalf("short input height=%d want 1", m.input.Height())
	}
}

// BLOCKER 3 — the widget's Width() is the single source of truth; height stays
// in sync across sidebar open/close and resize.
func TestCapInputHeight_WidgetWidthDrivesHeightAcrossGeometry(t *testing.T) {
	content := strings.Repeat("word ", 20)

	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 40
	m.setInputWidth()
	m.input.SetValue(content)
	fullRows := inputWrapped(m.input.Value(), m.input.Width())
	fullH := fullRows
	if fullH > inputMaxHeight {
		fullH = inputMaxHeight
	}
	m.capInputHeight()
	if m.input.Height() != fullH {
		t.Fatalf("full width: height=%d want %d (rows=%d w=%d)",
			m.input.Height(), fullH, fullRows, m.input.Width())
	}
	fullW := m.input.Width()

	// Open sidebar -> narrower main column -> more wrap rows.
	m.openSidebar(sidebarTabTokens)
	if w := m.input.Width(); w >= fullW {
		t.Fatalf("sidebar should narrow input width: full=%d side=%d", fullW, w)
	}
	rowsHalf := inputWrapped(m.input.Value(), m.input.Width())
	wantHalf := rowsHalf
	if wantHalf > inputMaxHeight {
		wantHalf = inputMaxHeight
	}
	if m.input.Height() != wantHalf {
		t.Fatalf("sidebar open: height=%d want %d (rows=%d w=%d)",
			m.input.Height(), wantHalf, rowsHalf, m.input.Width())
	}

	m.closeSidebar()
	if m.input.Width() != fullW {
		t.Fatalf("after close width=%d want %d", m.input.Width(), fullW)
	}
	m.capInputHeight()
	if m.input.Height() != fullH {
		t.Fatalf("after close height=%d want %d", m.input.Height(), fullH)
	}
}

// Resize also re-syncs the widget width and height via the same helper.
func TestCapInputHeight_ResizeReSyncs(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 40
	m.setInputWidth()
	m.input.SetValue(strings.Repeat("word ", 6))
	m.capInputHeight()
	beforeW, beforeH := m.input.Width(), m.input.Height()

	m.width = 16
	m.setInputWidth()
	m.capInputHeight()
	if m.input.Width() >= beforeW {
		t.Fatalf("resize narrower should reduce width: before=%d after=%d", beforeW, m.input.Width())
	}
	if m.input.Height() < beforeH {
		t.Fatalf("narrower width should not shrink height: before=%d after=%d", beforeH, m.input.Height())
	}

	m.width = 40
	m.setInputWidth()
	m.capInputHeight()
	if m.input.Height() != beforeH {
		t.Fatalf("widen back height=%d want %d", m.input.Height(), beforeH)
	}
}

// BLOCKER 4 — inserting a newline in the middle of a long wrapped line must
// produce the height the widget actually renders after the split.
func TestInsertNewline_MiddleOfLongLine(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 12
	m.setInputWidth()
	base := strings.Repeat("abc ", 12) // wraps to several rows
	m.input.SetValue(base)
	m.capInputHeight()
	before := m.input.Height()

	m.input.SetCursor(10)
	m = m.insertNewline()

	rowsAfter := inputWrapped(m.input.Value(), m.input.Width())
	want := rowsAfter
	if want > inputMaxHeight {
		want = inputMaxHeight
	}
	if m.input.Height() != want {
		t.Fatalf("after mid-line newline: height=%d want %d (rows=%d value=%q)",
			m.input.Height(), want, rowsAfter, m.input.Value())
	}
	if !strings.Contains(m.input.Value(), "\n") {
		t.Fatalf("newline not inserted: %q", m.input.Value())
	}
	if before < 2 || before > inputMaxHeight {
		t.Fatalf("unexpected pre-split height %d", before)
	}
}

func TestInsertNewline_SingleLine(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 30
	m.setInputWidth()
	m.input.SetValue("hello")
	m = m.insertNewline()
	if !strings.Contains(m.input.Value(), "\n") {
		t.Fatalf("insertNewline did not insert newline, value=%q", m.input.Value())
	}
	// Split into two hard lines, each one row -> total 2.
	if m.input.Height() != 2 {
		t.Fatalf("single-line split height=%d want 2", m.input.Height())
	}
}

// BLOCKER 2 — after growing past the cap, the cursor's wrapped row must be
// within the widget's rendered viewport (the last content row visible), not
// hidden above. Assert concrete rendered rows, not just Height().
func TestCapInputHeight_CursorRowVisiblePastCap(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 12
	m.setInputWidth()
	for _, r := range []rune(strings.Repeat("word ", 40)) {
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m.capInputHeight()
		_ = m.input.View()
	}
	if m.input.Height() != inputMaxHeight {
		t.Fatalf("want capped height %d, got %d", inputMaxHeight, m.input.Height())
	}
	// Type more; the cursor should be in the final rendered row region.
	for _, r := range []rune("tail") {
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m.capInputHeight()
		_ = m.input.View()
	}
	// Count visible content rows: with H capped and content ≫ H, the widget's
	// viewport shows H rows. The very last content the user typed ("tail") must
	// appear in the view (it is at the cursor / bottom).
	m.input.EndOfBufferCharacter = '#'
	view := m.input.View()
	visible := []string{}
	for _, l := range strings.Split(strings.TrimRight(view, "n"), "n") {
		if !strings.Contains(l, "#") {
			visible = append(visible, l)
		}
	}
	m.input.EndOfBufferCharacter = ' '
	if len(visible) == 0 {
		t.Fatalf("input rendered zero content rows")
	}
	if !strings.Contains(strings.Join(visible, "n"), "tail") {
		t.Fatalf("cursor/tail content not visible after cap; visible=%q", visible)
	}
}
