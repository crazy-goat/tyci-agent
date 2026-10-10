package display

import (
	"strings"
	"testing"
)

func TestCellWidth(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"empty string", "", 0},
		{"single ascii", "a", 1},
		{"simple ascii", "hello world", 11},
		{"ascii digits", "1234567890", 10},
		{"padded ascii", "hello world   ", 14},
		{"only spaces", "      ", 6},
		{"ascii newline single", "abc\ndef", 3},
		{"ascii newline first longer", "longer first line\nshort", 17},
		{"ascii newline second longer", "short\nlonger second line", 18},
		{"ascii crlf", "line1\r\nline222", 7},
		{"ascii multiple newlines", "a\nbb\nccc\nd", 3},
		{"ansi colored text", "\x1b[31mred\x1b[0m", 3},
		{"ansi bold and colored text", "\x1b[1m\x1b[31mbold red\x1b[0m", 8},
		{"ansi clear line", "a\x1b[Kb", 2},
		{"ansi multiline", "\x1b[31mabc\x1b[0m\n\x1b[32mdefgh\x1b[0m", 5},
		{"ansi with trailing reset", "\x1b[38;5;245m2026-10-10\x1b[0m", 10},
		{"cjk characters", "你好世界", 8},
		{"japanese characters", "日本語", 6},
		{"simple emoji", "💭", 2},
		{"emoji with skin tone", "👍🏽", 2},
		{"zwj family emoji", "👨‍👩‍👧‍👦", 2},
		{"accented letter precomposed", "café", 4},
		{"accented letter decomposed", "cafe\u0301", 4},
		{"devanagari word 1", "धन्यवाद", 6},
		{"devanagari word 2", "क्षत्रिय", 6},
		{"mixed ansi and unicode", "\x1b[32m✔ café\x1b[0m", 6},
		{"mixed symbols and box drawing", "─ │ ┌ ┐", 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cellWidth(tt.input)
			if got != tt.expected {
				t.Errorf("cellWidth(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestCellWidth_MatchesLayoutTokens(t *testing.T) {
	// Verifies that cellWidth returns identical results to layoutTokens
	// for a variety of inputs.
	samples := []string{
		"",
		"hello world",
		"1234567890abcdef",
		"\x1b[31m1234567890\x1b[0mabcdef",
		"\x1b[31m1234567890abcdef\x1b[0m",
		"💭1234567890abcdef",
		"abधन्यवाद",
		"धन्यवाद",
		"क्षत्रिय",
		"cafe\u0301",
		"a👍🏽b",
		"x👨‍👩‍👧‍👦y",
		"line1\nline2\nline3",
		"line1\r\nline2",
		"\x1b[31mline1\x1b[0m\n\x1b[32mlonger line 2\x1b[0m",
		strings.Repeat(" ", 80),
		strings.Repeat("─", 40),
	}

	for _, s := range samples {
		want := 0
		lineWidth := 0
		for _, tok := range layoutTokens(s) {
			if tok.newline {
				if lineWidth > want {
					want = lineWidth
				}
				lineWidth = 0
				continue
			}
			lineWidth += tok.width
		}
		if lineWidth > want {
			want = lineWidth
		}

		got := cellWidth(s)
		if got != want {
			t.Errorf("cellWidth(%q) = %d, want %d (from layoutTokens)", s, got, want)
		}
	}
}

func TestCellWidth_Allocations(t *testing.T) {
	cases := []string{
		"",
		"a",
		"hello world",
		"task/coder-01",
		"go test ./pkg001/...",
		"  No tasks recorded this session.       ",
		strings.Repeat(" ", 80),
		"cafe\u0301",
		"धन्यवाद",
		"👍🏽",
		"👨‍👩‍👧‍👦",
		"你好世界",
	}

	for _, s := range cases {
		allocs := testing.AllocsPerRun(100, func() {
			_ = cellWidth(s)
		})
		if allocs > 0 {
			t.Errorf("cellWidth(%q) allocated %f times, want 0", s, allocs)
		}
	}
}

func BenchmarkCellWidth_ASCII(b *testing.B) {
	s := "task/coder-01: go test ./pkg001/..."
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = cellWidth(s)
	}
}

func BenchmarkCellWidth_Padded(b *testing.B) {
	s := "  No tasks recorded this session.                                       "
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = cellWidth(s)
	}
}

func BenchmarkCellWidth_Unicode(b *testing.B) {
	s := "धन्यवाद क्षत्रिय cafe\u0301 👍🏽 👨‍👩‍👧‍👦"
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = cellWidth(s)
	}
}

func BenchmarkCellWidth_ANSI(b *testing.B) {
	s := "\x1b[31mred\x1b[0m \x1b[1mbold\x1b[0m \x1b[32mgreen\x1b[0m"
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = cellWidth(s)
	}
}
