package display

import "testing"

// cellWidthReference is the cellWidth that built layoutTokens for every call
// (#634). The fast cellWidth must give the same result (#636).
func cellWidthReference(s string) int {
	width, lineWidth := 0, 0
	for _, token := range layoutTokens(s) {
		if token.newline {
			if lineWidth > width {
				width = lineWidth
			}
			lineWidth = 0
			continue
		}
		lineWidth += token.width
	}
	if lineWidth > width {
		width = lineWidth
	}
	return width
}

func TestCellWidth_MatchesReference(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"ascii":             "hello world",
		"ascii max":         "~ !",
		"cjk":               "日本語のテキスト",
		"mixed cjk":         "ab日本cd",
		"emoji":             "😀 ok 🎉",
		"zwj":               "👨‍👩‍👧‍👦 family",
		"skin tone":         "👍🏽 yes",
		"flag":              "🇵🇱 PL",
		"combining":         "é à́ ok",
		"devanagari":        "क्षि हिन्दी",
		"tab":               "a\tb",
		"tab only":          "\t",
		"newline":           "short\nlonger line\nx",
		"crlf":              "ab\r\ncdef\r\ng",
		"lone cr":           "ab\rcd",
		"ansi color":        "\x1b[31mred\x1b[0m plain",
		"ansi cjk":          "\x1b[1m日本\x1b[0m語",
		"ansi in cluster":   "e\x1b[31ḿx",
		"ansi in zwj":       "👨\x1b[0m‍👩",
		"ansi only":         "\x1b[31m\x1b[0m",
		"ansi clear line":   "abc\x1b[K\ndef\x1b[2J",
		"ansi unterminated": "abc\x1b[31",
		"lone esc":          "abc\x1b",
		"esc other final":   "a\x1bXb\x1b[1;2mc",
		"invalid utf8":      "a\xffb\xc3",
		"control":           "a\x01\x7fb",
		"nbsp and zero":     "a b​c",
	}
	for name, s := range cases {
		if got, want := cellWidth(s), cellWidthReference(s); got != want {
			t.Errorf("%s: cellWidth(%q) = %d, reference %d", name, s, got, want)
		}
	}
}

func TestCellWidth_ASCIIDoesNotAllocate(t *testing.T) {
	s := "  ● worker 3   12s   1.2k   $0.04"
	if n := testing.AllocsPerRun(100, func() { _ = cellWidth(s) }); n != 0 {
		t.Errorf("cellWidth of printable ASCII allocated %v times", n)
	}
}
