package tools

import (
	"fmt"
	"sync"
)

// Bash output caps. Unlike the read tool (which caps what it *returns* but the
// caller only asks for a bounded slice), a bash command can stream an arbitrary
// amount to stdout/stderr — e.g. `cat huge.log`, `find /`, a chatty test run.
// Without a bound the whole thing is buffered in RAM, stored permanently in the
// conversation history, and re-sent to the model on every subsequent turn.
//
// We keep a head + tail window: the head preserves the start of the output
// (setup, first errors) and the tail preserves the end (final errors, exit
// summaries, test results) — the two regions a model actually needs. Everything
// in between is dropped with a marker noting the true size. Total retained bytes
// never exceed bashHeadMax + bashTailMax, matching the read tool's 256 KiB
// order of magnitude.
const (
	bashHeadMax = 128 * 1024 // 128 KiB kept from the start of the output
	bashTailMax = 128 * 1024 // 128 KiB kept from the end of the output
)

// cappedBuffer is an io.Writer that retains at most headMax bytes from the
// start and tailMax bytes from the end of everything written to it, regardless
// of total volume. It bounds the memory a single bash call can consume even
// while the command is still producing output (the writer is wired directly to
// the process pipes / the per-line accumulator).
type cappedBuffer struct {
	// mu guards every field. The streaming path drains stdout and stderr in
	// two separate goroutines, and a backgrounded command reports its size
	// while those goroutines are still writing, so this buffer is genuinely
	// concurrent — it was not when it only ever backed one io.Copy.
	mu         sync.Mutex
	head       []byte
	tail       []byte // sliding window of the most recent bytes
	headMax    int
	tailMax    int
	total_     int64 // total bytes ever written (for the truncation notice)
	overflowed bool
}

// newline is written after each streamed line; a package var avoids allocating
// a []byte on every line.
var newline = []byte("\n")

func newCappedBuffer(headMax, tailMax int) *cappedBuffer {
	return &cappedBuffer{headMax: headMax, tailMax: tailMax}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := len(p)
	b.total_ += int64(n)

	// Fill the head first, up to headMax.
	if len(b.head) < b.headMax {
		take := b.headMax - len(b.head)
		if take > len(p) {
			take = len(p)
		}
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	// Anything beyond the head goes into the sliding tail window.
	if len(p) > 0 {
		b.overflowed = true
		b.tail = append(b.tail, p...)
		// Keep only the last tailMax bytes. Copy into a fresh slice when the
		// backing array grows past 2*tailMax so it can't grow without bound
		// (reslicing alone keeps the old array alive).
		if len(b.tail) > b.tailMax {
			keep := b.tail[len(b.tail)-b.tailMax:]
			b.tail = append(make([]byte, 0, b.tailMax), keep...)
		}
	}
	return n, nil
}

// result assembles the retained output. When nothing was dropped it returns the
// exact bytes written; otherwise it joins the head and tail with a marker
// stating how much was elided.
func (b *cappedBuffer) result() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.overflowed {
		return string(b.head)
	}
	elided := b.total_ - int64(len(b.head)) - int64(len(b.tail))
	if elided < 0 {
		elided = 0
	}
	notice := fmt.Sprintf(
		"\n\n... [bash output truncated: %d bytes total, kept first %d and last %d, dropped %d in the middle] ...\n\n",
		b.total_, len(b.head), len(b.tail), elided,
	)
	return string(b.head) + notice + string(b.tail)
}

// total reports how many bytes have been written so far, including anything
// already dropped from the middle. Used for the size figure in a background
// command's completion notice, which is read while the command may still be
// writing.
func (b *cappedBuffer) total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total_
}
