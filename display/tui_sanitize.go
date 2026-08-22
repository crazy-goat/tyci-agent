package display

import (
	"strings"
	"unicode/utf8"
)

// Sanitising untrusted text before it enters the transcript.
//
// Everything a model writes and everything a command prints is arbitrary
// bytes, and it is drawn straight into a terminal that reads some of those
// bytes as commands. A probe of the render path found all of these arriving
// intact: ESC (so "\x1b[2J" clears the screen and "\x1b[H" moves the cursor
// out of the frame), CR (so a progress bar overwrites whatever the TUI drew),
// BEL, BS, VT and NUL. None of that is exotic input — `git diff --color`,
// `npm install`, and any model asked about terminal codes produce it.
//
// Sanitising has to happen on the way IN, not at render time: by then the
// TUI's own colour codes are in the same strings, and stripping escapes would
// take its styling with them.
//
// Colour is dropped rather than kept. A coloured tool result would fight the
// TUI's own palette, and there is no way to keep the harmless escapes without
// also keeping the ones that move the cursor — the same two-byte introducer
// starts both.
const (
	// tabWidth is what a tab becomes. Tabs are not passed through because the
	// selection and painter code counts columns, and a tab's width depends on
	// where it lands.
	tabWidth = 4
)

// sanitizeUntrusted makes a model's or a command's text safe to draw.
//
// Newlines survive — they are the one control character the transcript is
// built out of. Everything else is either translated (tab to spaces, CR to a
// newline) or dropped.
func sanitizeUntrusted(s string) string {
	if s == "" || !needsSanitizing(s) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			// Skip the whole escape sequence, not just the ESC: leaving
			// "[2J" behind would print visible litter instead of clearing
			// the screen.
			i += escapeLen(s[i:])
		case c == '\n':
			b.WriteByte('\n')
			i++
		case c == '\r':
			// CR alone is how a progress bar redraws its line. As part of
			// CRLF it is redundant. Either way a newline is the honest
			// rendering of "start again at the left".
			if i+1 < len(s) && s[i+1] == '\n' {
				i++ // let the LF below write the newline
				continue
			}
			b.WriteByte('\n')
			i++
		case c == '\t':
			b.WriteString(strings.Repeat(" ", tabWidth))
			i++
		case c < 0x20 || c == 0x7f:
			i++ // BEL, BS, VT, FF, NUL, DEL: dropped
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				// Invalid byte: replace rather than pass through, so the
				// terminal is never handed a broken sequence.
				b.WriteRune(utf8.RuneError)
				i++
				continue
			}
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return b.String()
}

// needsSanitizing is the fast path. Almost all text is clean, and scanning for
// a byte is far cheaper than rebuilding the string.
func needsSanitizing(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return !utf8.ValidString(s)
}

// escapeLen returns the length of the escape sequence at the start of s, which
// begins with ESC. It returns at least 1 so a caller always makes progress.
//
// Three forms are recognised, which between them cover what a terminal would
// act on: CSI ("\x1b[" then parameters then a final byte in @-~), the string
// sequences OSC/DCS/APC/PM (terminated by BEL or ST), and the two-byte escapes
// (ESC then a single byte, e.g. "\x1bc" for a full reset).
func escapeLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[': // CSI
		for i := 2; i < len(s); i++ {
			if s[i] >= '@' && s[i] <= '~' {
				return i + 1
			}
		}
		return len(s) // unterminated: drop the rest
	case ']', 'P', '_', '^': // OSC, DCS, APC, PM — terminated by BEL or ST
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	default:
		return 2
	}
}
