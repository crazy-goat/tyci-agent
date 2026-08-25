// Package display — terminal input sanitization for the TUI.
//
// bubbletea's input parser expects mouse SGR escape sequences to begin with
// the 0x1b (ESC) byte ("\x1b[<button;col;rowM"). In practice the leading ESC
// is sometimes dropped before the bytes reach us — for example, terminals
// running in legacy X10 emulation, terminal-side races around our SGR-mode
// enable, or shells whose scrollback captures and re-pastes raw event bytes.
// When the ESC byte is missing, the remaining payload "[<button;col;rowM"
// is read by bubbletea as ordinary KeyRunes and the textarea displays the
// gibberish verbatim.
//
// sanitizeInput strips stray mouse escapes (those missing the leading 0x1b
// byte) before they reach bubbletea, while passing everything else through
// unchanged. Real SGR mouse events with their intact 0x1b prefix still
// become MouseMsg and scroll/select still work.
//
// The reader holds back the trailing few bytes of a Read whenever they could
// be the prefix of a stray SGR mouse that completes in the next chunk.
package display

import "io"

// sanitizeInput wraps r with a Reader that drops stray SGR mouse escapes
// (those missing the leading 0x1b byte) so they never reach bubbletea as
// raw KeyRunes.
func sanitizeInput(r io.Reader) io.Reader {
	return &sanitizeReader{inner: r}
}

type sanitizeReader struct {
	inner io.Reader
	// pending is a trailing mouse prefix that may continue in the next chunk.
	// It is kept separate from ready so an already-filtered real SGR escape is
	// never inspected again after a small caller buffer splits its output.
	pending []byte
	ready   []byte
	done    bool
	err     error
}

const sgrMouseMaxLen = 24

func (s *sanitizeReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	for {
		if len(s.ready) > 0 {
			n := copy(p, s.ready)
			s.ready = s.ready[n:]
			return n, nil
		}
		if s.done {
			if len(s.pending) > 0 {
				n := copy(p, s.pending)
				s.pending = s.pending[n:]
				return n, nil
			}
			return 0, s.err
		}

		maxSrc := len(p) + sgrMouseMaxLen
		joined := append([]byte(nil), s.pending...)
		s.pending = s.pending[:0]
		if len(joined) > maxSrc {
			// This should not happen with the bounded source reads below, but
			// avoid passing a negative slice bound to an unusual caller.
			maxSrc = len(joined)
		}
		src := make([]byte, maxSrc)
		n, err := s.inner.Read(src[:maxSrc-len(joined)])
		joined = append(joined, src[:n]...)
		if err != nil {
			s.done = true
			s.err = err
		}

		out, deferTail := filterStrayMouseWithDefer(joined, sgrMouseMaxLen)
		copied := copy(p, out)
		// Output that did not fit must precede a deferred raw prefix. The
		// latter is only safe to inspect after the next source chunk arrives.
		s.ready = append(s.ready, out[copied:]...)
		s.pending = append(s.pending, deferTail...)
		if copied > 0 {
			// Do not return EOF alongside bytes that are still queued: callers
			// are allowed to stop at n>0, err==EOF and would lose those bytes.
			return copied, nil
		}
		if len(s.pending) > 0 {
			if s.done {
				// There is no future chunk that could complete this prefix, so
				// flush it verbatim as a best-effort EOF behavior.
				continue
			}
			// The input chunk was entirely held back; read the next chunk.
			continue
		}
		if s.done {
			return 0, s.err
		}
		// A complete stray escape may consume the whole chunk without
		// producing output. Return control to the caller rather than blocking
		// for another source chunk in the same Read call.
		if n > 0 {
			return 0, nil
		}
		// Preserve an unusual inner Reader's (0, nil) result rather than
		// spinning forever.
		return 0, nil
	}
}

// filterStrayMouseWithDefer filters complete stray mouse escapes and holds
// back a trailing prefix that may continue in the next Read. A real SGR
// prefix is deferred from its ESC; a stray prefix is deferred from its `[`.
// Keeping the ESC with the real prefix is essential: otherwise the next Read
// receives only `[<...` and bubbletea sees it as ordinary text.
func filterStrayMouseWithDefer(src []byte, maxSpill int) (kept []byte, deferTail []byte) {
	if len(src) == 0 {
		return nil, nil
	}
	if maxSpill <= 0 {
		maxSpill = len(src)
	}
	lookback := len(src) - maxSpill
	if lookback < 0 {
		lookback = 0
	}

	out := make([]byte, 0, len(src))
	for i := 0; i < len(src); {
		if src[i] == 0x1b {
			end, status := sgrMouseMatchAt(src, i)
			switch status {
			case strayMatchComplete:
				out = append(out, src[i:end]...)
				i = end
				continue
			case strayMatchSpills:
				if i >= lookback {
					return out, append([]byte(nil), src[i:]...)
				}
			}
		}
		if src[i] == '[' && (i == 0 || src[i-1] != 0x1b) {
			end, status := strayMatchAt(src, i)
			switch status {
			case strayMatchComplete:
				// A no-ESC mouse payload is the one sequence this filter drops.
				i = end
				continue
			case strayMatchSpills:
				if i >= lookback {
					return out, append([]byte(nil), src[i:]...)
				}
			}
		}
		out = append(out, src[i])
		i++
	}
	return out, nil
}

type strayMatchStatus int

const (
	strayMatchNone strayMatchStatus = iota
	strayMatchComplete
	strayMatchSpills
)

// strayMatchAt tries to match a stray SGR mouse escape starting at the `[`
// at offset `idx` in src. Returns (end, status). status describes whether
// and how the pattern matched.
func strayMatchAt(src []byte, idx int) (int, strayMatchStatus) {
	return mouseMatchAt(src, idx, false)
}

// sgrMouseMatchAt is the corresponding matcher for a real SGR mouse escape,
// whose prefix starts with ESC. Incomplete prefixes are reported as spills so
// the caller can retain the ESC across Read boundaries.
func sgrMouseMatchAt(src []byte, idx int) (int, strayMatchStatus) {
	return mouseMatchAt(src, idx, true)
}

func mouseMatchAt(src []byte, idx int, withESC bool) (int, strayMatchStatus) {
	if withESC {
		if idx >= len(src) || src[idx] != 0x1b {
			return 0, strayMatchNone
		}
		idx++
		if idx >= len(src) {
			return 0, strayMatchSpills
		}
	}
	if idx >= len(src) || src[idx] != '[' {
		return 0, strayMatchNone
	}
	idx++
	if idx >= len(src) {
		return 0, strayMatchSpills
	}
	if src[idx] != '<' {
		return 0, strayMatchNone
	}
	idx++

	for field := 0; field < 3; field++ {
		fieldStart := idx
		for idx < len(src) && isDigit(src[idx]) {
			idx++
		}
		if idx == fieldStart {
			if idx == len(src) {
				return 0, strayMatchSpills
			}
			return 0, strayMatchNone
		}
		if field == 2 {
			break
		}
		if idx == len(src) {
			return 0, strayMatchSpills
		}
		if src[idx] != ';' {
			return 0, strayMatchNone
		}
		idx++
	}
	if idx == len(src) {
		return 0, strayMatchSpills
	}
	if src[idx] != 'M' && src[idx] != 'm' {
		return 0, strayMatchNone
	}
	return idx + 1, strayMatchComplete
}

// filterStrayMouse returns a copy of buf with stray SGR mouse escapes
// (those missing the leading 0x1b byte) replaced by nothing. Sequences that
// DO begin with ESC \x1b[<...M are passed through untouched. The output is
// always at most len(buf) bytes long.
func filterStrayMouse(buf []byte) []byte {
	out := make([]byte, 0, len(buf))
	i := 0
	for i < len(buf) {
		// Real SGR mouse escape with its ESC byte — pass through.
		if i+2 < len(buf) && buf[i] == 0x1b && buf[i+1] == '[' && buf[i+2] == '<' {
			end := sgrMouseEnd(buf, i+3)
			if end > 0 {
				out = append(out, buf[i:end]...)
				i = end
				continue
			}
		}
		// Stray (no-leading-ESC) mouse escape starting at `[`.
		if buf[i] == '[' && (i == 0 || buf[i-1] != 0x1b) {
			if end, status := strayMatchAt(buf, i); status == strayMatchComplete {
				i = end
				continue
			}
		}
		out = append(out, buf[i])
		i++
	}
	return out
}

// sgrMouseEnd returns the byte index just past the SGR mouse escape that
// starts at offset `start` (the byte immediately after the `[<`). Returns 0
// if the pattern doesn't match within buf. Used for the leading-ESC escape
// path; the no-ESC variant goes through strayMatchAt above.
func sgrMouseEnd(buf []byte, start int) int {
	j := start
	if j >= len(buf) || !isDigit(buf[j]) {
		return 0
	}
	for j < len(buf) && isDigit(buf[j]) {
		j++
	}
	if j >= len(buf) || buf[j] != ';' {
		return 0
	}
	j++
	if j >= len(buf) || !isDigit(buf[j]) {
		return 0
	}
	for j < len(buf) && isDigit(buf[j]) {
		j++
	}
	if j >= len(buf) || buf[j] != ';' {
		return 0
	}
	j++
	if j >= len(buf) || !isDigit(buf[j]) {
		return 0
	}
	for j < len(buf) && isDigit(buf[j]) {
		j++
	}
	if j >= len(buf) || (buf[j] != 'M' && buf[j] != 'm') {
		return 0
	}
	return j + 1
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
