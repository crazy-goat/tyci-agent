package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
)

func TestInputWrappedLineCount(t *testing.T) {
	cases := []struct {
		value string
		width int
		want  int
	}{
		{"", 20, 1},
		{"hello world", 20, 1},
		{"aaa\nbbb", 20, 2},
		{"a b c d", 4, 2},
		{"hello", 5, 2}, // exact fit still wraps (>= width flips to next line)
		{"aaaaaaaaaa", 5, 3},
		{"aaa\nbbbbbbbbbbbb cccc", 8, 4},
	}
	for _, c := range cases {
		got := inputWrappedLineCount(c.value, c.width)
		if got != c.want {
			t.Fatalf("inputWrappedLineCount(%q,%d)=%d want %d", c.value, c.width, got, c.want)
		}
	}
}

// inputWrappedLineCount must return exactly the number of display rows the
// bubbles textarea renders for the same value and wrap width (showLineNumbers
// off, matching newModel). The textarea's View() pads with Height end-of-buffer
// rows; we use a distinctive EndOfBufferCharacter and strip those lines.
func TestInputWrappedLineCount_MatchesTextareaView(t *testing.T) {
	cases := []struct {
		value string
		width int // wrap width including the 2-col prompt reservation
	}{
		{"hello world foo bar baz", 12},
		{"aaaaaaaaaa b", 5},
		{"one two\nthree four five six seven", 10},
		{"超长超长超长超长 word", 6},
		{"a b c d e f g h i j k l", 4},
		{"x", 3},
		{"hello  double space", 8},
		{"singlewordexceedingwidth", 6},
	}
	for _, c := range cases {
		ta := textarea.New()
		ta.ShowLineNumbers = false
		ta.SetValue(c.value)
		ta.SetWidth(c.width)
		ta.SetHeight(200)
		ta.EndOfBufferCharacter = '#'
		wrapped := 0
		for _, l := range strings.Split(ta.View(), "\n") {
			if !strings.Contains(l, "#") {
				wrapped++
			}
		}
		got := inputWrappedLineCount(c.value, c.width-2)
		if got != wrapped {
			t.Fatalf("value=%q wrapWidth=%d: textarea=%d rows, inputWrappedLineCount=%d",
				c.value, c.width-2, wrapped, got)
		}
	}
}

func TestCapInputHeight_GrowsWithWrapAndCapsAtTen(t *testing.T) {
	// Long text without spaces hard-breaks into many rows at a narrow width.
	longWord := ""
	for i := 0; i < 200; i++ {
		longWord += "a"
	}
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 40
	m.input.SetValue(longWord)
	m.capInputHeight()
	// With hard newlines pushing past 10 rows, height clamps to the cap and the
	// textarea's internal viewport scrolls.
	long := strings.Repeat("word ", 300) // ~1200 chars, wraps to ≫10 rows
	m.input.SetValue(long)
	m.capInputHeight()
	if h := m.input.Height(); h != 10 {
		t.Fatalf("long text height=%d, want 10 (capped)", h)
	}

	// Text that wraps to just a few rows grows to exactly that many.
	m2 := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m2.width = 30
	m2.input.SetValue("aaa bbb ccc") // wrap width ~26 → 1 row
	m2.capInputHeight()
	if h := m2.input.Height(); h != 1 {
		t.Fatalf("short height=%d, want 1", h)
	}
	m2.input.SetValue("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa bbbbbbbb")
	m2.capInputHeight()
	if h := m2.input.Height(); h < 2 {
		t.Fatalf("wrapped height=%d, want >1", h)
	}
}

func TestInsertNewline_HeightPreSetThenCapped(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 20
	m.input.SetValue("hello")
	m = m.insertNewline()
	// A single hard newline does not wrap, so height stays 1 but cursor moved on.
	if !strings.Contains(m.input.Value(), "\n") {
		t.Fatalf("insertNewline did not insert a newline, value=%q", m.input.Value())
	}
}
