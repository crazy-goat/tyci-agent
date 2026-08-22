package display

import (
	"strings"
	"testing"
)

func TestSanitizeLeavesOrdinaryTextAlone(t *testing.T) {
	for _, in := range []string{
		"",
		"hello world",
		"line one\nline two\n",
		"func main() {\n\treturn\n}", // the tab is the exception, checked below
		"unicode: żółć 中文 🙂",
		"pipes | and <angles> and [brackets]",
	} {
		got := sanitizeUntrusted(in)
		want := strings.ReplaceAll(in, "\t", strings.Repeat(" ", tabWidth))
		if got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// TestSanitizeRemovesEscapeSequences is the vulnerability this closes. A probe
// of the render path found every one of these reaching the terminal intact,
// and none of them is exotic input: `git diff --color`, `npm install` and any
// model asked about terminal codes all produce them.
func TestSanitizeRemovesEscapeSequences(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"clear screen", "before\x1b[2Jafter", "beforeafter"},
		{"cursor home", "before\x1b[Hafter", "beforeafter"},
		{"colour", "before\x1b[31mred\x1b[0mafter", "beforeredafter"},
		{"alt screen", "a\x1b[?1049hb", "ab"},
		{"scroll region", "a\x1b[1;5rb", "ab"},
		{"full reset", "a\x1bcb", "ab"},
		{"osc title bel", "a\x1b]0;new title\x07b", "ab"},
		{"osc title st", "a\x1b]0;new title\x1b\\b", "ab"},
		{"unterminated csi", "a\x1b[38;5;", "a"},
	}
	for _, tc := range cases {
		if got := sanitizeUntrusted(tc.in); got != tc.want {
			t.Errorf("%s: %q -> %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestSanitizeTurnsCarriageReturnIntoANewline: a lone CR is how a progress bar
// redraws its line, and in the middle of a transcript that means overwriting
// whatever the TUI drew there.
func TestSanitizeTurnsCarriageReturnIntoANewline(t *testing.T) {
	if got := sanitizeUntrusted("50%\r100%"); got != "50%\n100%" {
		t.Errorf("got %q", got)
	}
	// CRLF is one line ending, not two.
	if got := sanitizeUntrusted("a\r\nb"); got != "a\nb" {
		t.Errorf("CRLF became %q", got)
	}
}

func TestSanitizeDropsOtherControlCharacters(t *testing.T) {
	in := "a\x00b\ac\bd\ve\ff\x7fg"
	if got := sanitizeUntrusted(in); got != "abcdefg" {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeExpandsTabs(t *testing.T) {
	if got := sanitizeUntrusted("a\tb"); got != "a"+strings.Repeat(" ", tabWidth)+"b" {
		t.Errorf("got %q", got)
	}
}

// TestSanitizeReplacesInvalidUTF8 rather than passing it through: a broken
// sequence handed to the terminal can swallow the bytes that follow it.
func TestSanitizeReplacesInvalidUTF8(t *testing.T) {
	got := sanitizeUntrusted("ok\xffmore")
	if strings.Contains(got, "\xff") {
		t.Errorf("invalid byte survived: %q", got)
	}
	if !strings.Contains(got, "ok") || !strings.Contains(got, "more") {
		t.Errorf("surrounding text was lost: %q", got)
	}
}

// TestBlockContentIsSanitizedOnTheWayIn covers the wiring: sanitising the
// function is no use if a kind of message bypasses it.
func TestBlockContentIsSanitizedOnTheWayIn(t *testing.T) {
	for _, kind := range []string{"text", "thinking", "block", "error"} {
		m := newPickerTestModel(testProviders, nil, "")
		m.handleBlockMsg(tuiMsgBlock{kind: kind, content: "before\x1b[2J\rafter"})

		region := m.buildMessageRegion(m.messageRegionHeight())
		if strings.Contains(region, "\x1b[2J") {
			t.Errorf("%s: a clear-screen sequence reached the frame", kind)
		}
		if strings.Contains(region, "\r") {
			t.Errorf("%s: a carriage return reached the frame", kind)
		}
	}
}

// TestToolOutputIsSanitized: tool output is the likeliest source in practice.
// Any command run with a colour flag emits escapes, and a progress bar emits
// carriage returns.
func TestToolOutputIsSanitized(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "bash"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-progress", toolIdx: 0, content: "\x1b[32mpassed\x1b[0m\rredraw"})

	out := m.blocks[0].output
	if strings.Contains(out, "\x1b") || strings.Contains(out, "\r") {
		t.Fatalf("tool output kept its control characters: %q", out)
	}
	if !strings.Contains(out, "passed") {
		t.Fatalf("the text itself was lost: %q", out)
	}
}
