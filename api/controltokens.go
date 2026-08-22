package api

import "strings"

// Filtering of model control tokens out of streamed text.
//
// Models are trained with template markers that the serving layer is supposed
// to consume. When a gateway forgets to strip them they arrive as ordinary
// content and land on screen and in the conversation history, where they read
// as corrupted output and teach the model to emit more of the same.
//
// Two shapes are recognised, because between them they cover every family
// that turns up in practice:
//
//	<|...|>          ChatML and its descendants: <|im_start|>, <|im_end|>
//	                 (Qwen, Yi), <|eot_id|>, <|start_header_id|> (Llama 3),
//	                 <|endoftext|>, <|tool_call|>.
//	｜...｜  / <｜...｜...>   DeepSeek, which delimits with U+FF5C, the
//	                 FULLWIDTH VERTICAL LINE: <｜DSML｜parameter>,
//	                 <｜begin_of_sentence｜>.
//
// Both openers are safe to key on. U+FF5C appears in no code or prose these
// models emit — that is why it was chosen as a delimiter. The ASCII "<|" is
// not valid in any mainstream language, and requiring the matching "|>" to
// close it rules out the one place a bare "|" is common (shell pipes, Go
// operators, markdown tables).
//
// They cannot be filtered per-delta, because a delta boundary falls anywhere:
// one real stream delivered "<", then "｜DSML｜", then "parameter>" as three
// separate deltas. So this is a small state machine that holds back the tail
// of the stream while a marker might still be forming.
//
// The bias throughout is towards showing text rather than swallowing it: an
// unterminated marker longer than maxHeldRunes is released verbatim, and Flush
// releases whatever is still held when the stream ends. A false negative is a
// few odd characters on screen; a false positive silently eats the model's
// answer.
const (
	// fullwidthPipe delimits DeepSeek-family markers.
	fullwidthPipe = "｜"

	// asciiOpen and asciiClose delimit ChatML-family markers.
	asciiOpen  = "<|"
	asciiClose = "|>"

	// maxHeldRunes bounds how much may be withheld while a marker is
	// incomplete. Real markers are short; anything longer is ordinary text
	// that happened to contain the delimiter.
	maxHeldRunes = 64
)

// controlTokenFilter removes control markers from a stream of text deltas.
// One instance per stream (text and reasoning need their own, since each has
// its own delta sequence).
type controlTokenFilter struct {
	held string
}

// Feed takes the next delta and returns the text safe to pass on, which may be
// empty (everything is still being held) or longer than the input (a hold was
// resolved).
func (f *controlTokenFilter) Feed(delta string) string {
	f.held += delta

	var out strings.Builder
	for {
		start, closer, contentAt, ok := nextMarker(f.held)
		if !ok {
			// No marker in sight. Emit everything except a trailing "<" or
			// "</", which could be the start of one arriving in the next
			// delta.
			keep := trailingMarkerStart(f.held)
			out.WriteString(f.held[:len(f.held)-keep])
			f.held = f.held[len(f.held)-keep:]
			return out.String()
		}

		out.WriteString(f.held[:start])
		rest := f.held[contentAt:]

		j := strings.Index(rest, closer)
		if j < 0 {
			// Still forming. Hold it — unless it has grown past the point
			// where it could plausibly be a marker, in which case the text
			// was never a marker and must not be lost.
			f.held = f.held[start:]
			if len([]rune(f.held)) > maxHeldRunes {
				out.WriteString(f.held)
				f.held = ""
			}
			return out.String()
		}
		f.held = rest[j+len(closer):]
	}
}

// nextMarker locates the earliest marker opener in s and describes it:
// start is where the marker begins (including any "<" or "</" that introduces
// it), contentAt is where to search for the closer, and closer is what ends
// this marker's shape.
func nextMarker(s string) (start int, closer string, contentAt int, ok bool) {
	iAscii := strings.Index(s, asciiOpen)
	iWide := strings.Index(s, fullwidthPipe)

	switch {
	case iAscii < 0 && iWide < 0:
		return 0, "", 0, false
	case iWide < 0 || (iAscii >= 0 && iAscii < iWide):
		// <|name|>
		return iAscii, asciiClose, iAscii + len(asciiOpen), true
	}

	// A fullwidth marker may be introduced by "<" or "</" immediately before
	// the delimiter, in which case it runs to the closing ">"; on its own it
	// is closed by a second delimiter.
	switch {
	case strings.HasSuffix(s[:iWide], "</"):
		return iWide - 2, ">", iWide + len(fullwidthPipe), true
	case strings.HasSuffix(s[:iWide], "<"):
		return iWide - 1, ">", iWide + len(fullwidthPipe), true
	default:
		return iWide, fullwidthPipe, iWide + len(fullwidthPipe), true
	}
}

// Flush releases anything still held, for the end of a stream: a partial
// marker that never completed was ordinary text after all.
func (f *controlTokenFilter) Flush() string {
	out := f.held
	f.held = ""
	return out
}

// trailingMarkerStart reports how many bytes at the end of s could be the
// beginning of a marker ("<" or "</") and must therefore be held back.
func trailingMarkerStart(s string) int {
	switch {
	case strings.HasSuffix(s, "</"):
		return 2
	case strings.HasSuffix(s, "<"):
		return 1
	}
	return 0
}
