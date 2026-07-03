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
	// pending is a small buffer of bytes that look like the prefix of a
	// stray mouse pattern but didn't terminate in the previous Read. We
	// prepend it to the next chunk before running the filter. On EOF we
	// flush it verbatim (best effort).
	pending []byte
}

const sgrMouseMaxLen = 24

func (s *sanitizeReader) Read(p []byte) (int, error) {
	// Pull a fresh chunk from the inner stream and prepend any pending.
	maxSrc := len(p) + sgrMouseMaxLen
	src := make([]byte, maxSrc)

	// If we already have pending bytes stashed, joining them is sufficient;
	// skip the inner Read until pending is drained.
	if len(s.pending) > 0 {
		joined := append([]byte(nil), s.pending...)
		s.pending = s.pending[:0]
		// Read whatever's available; non-blocking semantics aren't needed
		// because bubbletea reads synchronously.
		n, err := s.inner.Read(src[:maxSrc-len(joined)])
		joined = append(joined, src[:n]...)
		out, deferTail := filterStrayMouseWithDefer(joined, sgrMouseMaxLen)
		s.pending = append(s.pending, deferTail...)
		copied := copy(p, out)
		if copied < len(out) {
			s.pending = append(s.pending, out[copied:]...)
		}
		if err != nil && copied == 0 {
			return 0, err
		}
		// Even on EOF we don't worry about flushing pending that was already
		// joined: anything stashed in s.pending above is bytes that we
		// couldn't safely emit yet because they MIGHT continue into a stray
		// pattern in the (now empty) input. We let the next call to Read
		// surface them as text when EOF has already been delivered.
		if err == io.EOF && len(s.pending) > 0 {
			// Drain pending now since no more inner data is coming.
			n2 := copy(p[copied:], s.pending)
			s.pending = s.pending[n2:]
			copied += n2
		}
		return copied, err
	}

	n, err := s.inner.Read(src)
	src = src[:n]
	out, deferTail := filterStrayMouseWithDefer(src, sgrMouseMaxLen)
	s.pending = append(s.pending, deferTail...)
	copied := copy(p, out)
	if copied < len(out) {
		s.pending = append(s.pending, out[copied:]...)
	}
	if err != nil && copied == 0 {
		return 0, err
	}
	return copied, err
}

// filterStrayMouseWithDefer runs filterStrayMouse over src and then examines
// the trailing bytes for a possible spill across reads. Returns (kept,
// deferTail). deferTail is empty when nothing needs to be deferred.
func filterStrayMouseWithDefer(src []byte, maxSpill int) (kept []byte, deferTail []byte) {
	cleaned := filterStrayMouse(src)
	if len(cleaned) == 0 {
		return cleaned, nil
	}
	// Walk back through cleaned looking for the rightmost `[` whose stray
	// pattern would still match (i.e. could continue past the chunk end).
	// Only consider the last maxSpill bytes — anything further left is
	// safe to emit because the longest possible SGR mouse pattern fits in
	// that window.
	startLookback := len(cleaned) - maxSpill
	if startLookback < 0 {
		startLookback = 0
	}
	for i := len(cleaned) - 1; i >= startLookback; i-- {
		if cleaned[i] != '[' {
			continue
		}
		if i+1 >= len(cleaned) || cleaned[i+1] != '<' {
			return cleaned, nil
		}
		_, status := strayMatchAt(cleaned, i)
		switch status {
		case strayMatchNone:
			return cleaned, nil
		case strayMatchComplete:
			// The pattern is fully visible in this chunk and was already
			// stripped by filterStrayMouse. There's no risk of spill.
			return cleaned, nil
		case strayMatchSpills:
			return append([]byte(nil), cleaned[:i]...), append([]byte(nil), cleaned[i:]...)
		}
	}
	return cleaned, nil
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
	if idx+1 >= len(src) || src[idx+1] != '<' {
		return 0, strayMatchNone
	}
	j := idx + 2
	for j < len(src) && isDigit(src[j]) {
		j++
	}
	if j == idx+2 {
		return 0, strayMatchNone
	}
	if j >= len(src) {
		return 0, strayMatchSpills
	}
	if src[j] != ';' {
		return 0, strayMatchNone
	}
	j++
	for j < len(src) && isDigit(src[j]) {
		j++
	}
	if j >= len(src) {
		return 0, strayMatchSpills
	}
	if src[j] != ';' {
		return 0, strayMatchNone
	}
	j++
	for j < len(src) && isDigit(src[j]) {
		j++
	}
	if j >= len(src) {
		return 0, strayMatchSpills
	}
	if src[j] != 'M' && src[j] != 'm' {
		return 0, strayMatchNone
	}
	return j + 1, strayMatchComplete
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
