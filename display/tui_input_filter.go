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
// The reader holds back the trailing bytes of an input chunk whenever they
// could be the start of a mouse escape that completes in the next chunk. A
// held prefix is released after sanitizeHoldTime, so a lone Esc key still
// reaches bubbletea. A Read never cuts a sequence in two: bubbletea parses
// each read on its own, and a split sequence becomes ordinary text.
//
// Text inside a bracketed paste is passed through unchanged, so a pasted
// "[<1;2;3M" is inserted. A mouse fragment that still reaches bubbletea as
// typed text is dropped by isStrayMouseText in Update.
package display

import (
	"bytes"
	"io"
	"regexp"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// sanitizeInput wraps r with a Reader that drops stray SGR mouse escapes
// (those missing the leading 0x1b byte) so they never reach bubbletea as
// raw KeyRunes.
func sanitizeInput(r io.Reader) io.Reader {
	return &sanitizeReader{inner: r}
}

// sanitizeChunkSize is the largest read from the input. bubbletea reads 256
// bytes at a time. A chunk and the held prefix (at most sgrMouseMaxLen bytes)
// then fit in one such Read, so no Read splits a sequence.
const sanitizeChunkSize = 256 - sgrMouseMaxLen

// sanitizeHoldTime is how long a held prefix waits for its rest. A terminal
// writes a sequence at once, so the rest normally arrives at once.
const sanitizeHoldTime = 50 * time.Millisecond

const sgrMouseMaxLen = 24

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

type sanitizeChunk struct {
	data []byte
	err  error
}

type sanitizeReader struct {
	inner io.Reader
	// chunks carries the reads of inner from readLoop. It is nil until the
	// first Read starts readLoop.
	chunks chan sanitizeChunk
	// pending is a trailing mouse prefix that may continue in the next chunk.
	// It is kept separate from ready so an already-filtered real SGR escape is
	// never inspected again after a small caller buffer splits its output.
	pending []byte
	ready   []byte
	// inPaste is true between a bracketed paste start and end marker.
	inPaste bool
	// err is the error of inner. It is returned after ready and pending.
	err error
}

// readLoop reads inner in the background, so Read can stop waiting for a held
// prefix after sanitizeHoldTime.
func (s *sanitizeReader) readLoop() {
	for {
		buf := make([]byte, sanitizeChunkSize)
		n, err := s.inner.Read(buf)
		s.chunks <- sanitizeChunk{data: buf[:n], err: err}
		if err != nil {
			return
		}
	}
}

func (s *sanitizeReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s.chunks == nil {
		s.chunks = make(chan sanitizeChunk)
		go s.readLoop()
	}

	for len(s.ready) == 0 {
		if s.err != nil {
			if len(s.pending) == 0 {
				return 0, s.err
			}
			// No more input can complete the held prefix: release it.
			s.ready, s.pending = s.pending, nil
			continue
		}

		var expired <-chan time.Time
		var timer *time.Timer
		if len(s.pending) > 0 {
			timer = time.NewTimer(sanitizeHoldTime)
			expired = timer.C
		}
		select {
		case c := <-s.chunks:
			if timer != nil {
				timer.Stop()
			}
			s.err = c.err
			s.filter(c.data)
		case <-expired:
			// No rest of the sequence came in time: it was not a split
			// sequence, so release it (a lone Esc key, for example).
			s.ready, s.pending = s.pending, nil
		}
	}

	n := copy(p, s.ready)
	s.ready = s.ready[n:]
	return n, nil
}

// filter filters a chunk after the held prefix, queues the output in ready
// and holds back a new trailing prefix in pending.
func (s *sanitizeReader) filter(data []byte) {
	joined := append(s.pending, data...)
	s.pending = nil
	out, deferTail, inPaste := filterStrayMouseWithDefer(joined, sgrMouseMaxLen, s.inPaste)
	s.ready = append(s.ready, out...)
	s.pending = deferTail
	s.inPaste = inPaste
}

// filterStrayMouseWithDefer filters complete stray mouse escapes and holds
// back a trailing prefix that may continue in the next Read. A real SGR
// prefix is deferred from its ESC; a stray prefix is deferred from its `[`.
// Keeping the ESC with the real prefix is essential: otherwise the next Read
// receives only `[<...` and bubbletea sees it as ordinary text. Inside a
// bracketed paste nothing is dropped. paste is the paste state at the start of
// deferTail, which the caller passes to the next call.
func filterStrayMouseWithDefer(src []byte, maxSpill int, inPaste bool) (kept []byte, deferTail []byte, paste bool) {
	if len(src) == 0 {
		return nil, nil, inPaste
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
			if i >= lookback && (isPrefixOf(src[i:], pasteStart) || isPrefixOf(src[i:], pasteEnd)) {
				return out, append([]byte(nil), src[i:]...), inPaste
			}
			if bytes.HasPrefix(src[i:], pasteStart) {
				inPaste = true
				out = append(out, pasteStart...)
				i += len(pasteStart)
				continue
			}
			if bytes.HasPrefix(src[i:], pasteEnd) {
				inPaste = false
				out = append(out, pasteEnd...)
				i += len(pasteEnd)
				continue
			}
			end, status := sgrMouseMatchAt(src, i)
			switch status {
			case strayMatchComplete:
				out = append(out, src[i:end]...)
				i = end
				continue
			case strayMatchSpills:
				if i >= lookback {
					return out, append([]byte(nil), src[i:]...), inPaste
				}
			}
		}
		if !inPaste && src[i] == '[' && (i == 0 || src[i-1] != 0x1b) {
			end, status := strayMatchAt(src, i)
			switch status {
			case strayMatchComplete:
				// A no-ESC mouse payload is the one sequence this filter drops.
				i = end
				continue
			case strayMatchSpills:
				if i >= lookback {
					return out, append([]byte(nil), src[i:]...), inPaste
				}
			}
		}
		out = append(out, src[i])
		i++
	}
	return out, nil, inPaste
}

// isPrefixOf reports whether a is a proper prefix of b.
func isPrefixOf(a, b []byte) bool {
	return len(a) < len(b) && bytes.HasPrefix(b, a)
}

// strayMouseText matches text made only of SGR mouse fragments: an optional
// cut tail of an escape ("1;35M", the end of an escape split by a read) and
// whole escapes without their ESC ("[<65;71;35M").
var strayMouseText = regexp.MustCompile(`^(?:(?:\d+;)*\d+;\d+[Mm])?(?:\[<\d+;\d+;\d+[Mm])*$`)

// isStrayMouseText reports whether a message is typed text made only of SGR
// mouse fragments. A pasted message is never a stray mouse event, so it is
// not matched.
func isStrayMouseText(msg tea.Msg) bool {
	key, ok := msg.(tea.KeyMsg)
	return ok && key.Type == tea.KeyRunes && !key.Paste && len(key.Runes) > 0 &&
		strayMouseText.MatchString(string(key.Runes))
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
