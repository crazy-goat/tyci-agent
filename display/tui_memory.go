package display

import (
	"runtime/debug"
	"strings"
	"time"
)

// freeOSMemoryInterval is the minimum time between two forced heap scavenges.
// See maybeFreeOSMemory.
const freeOSMemoryInterval = 10 * time.Second

// releaseOSMemory starts the heap scavenge. It is a variable so that tests can
// count the calls. The scavenge can take a few ms, so it runs on its own
// goroutine, not on the UI goroutine.
var releaseOSMemory = func() { go debug.FreeOSMemory() }

// maybeFreeOSMemory hands memory that was dropped by the scrollback cache
// (flushed block content, cachedLines and tool output) back to the OS. Go's GC
// frees objects but does not scavenge the heap by itself, so RSS would not come
// down over a long session. Call it only after blocks were flushed to disk. A
// streamed flush does not change the heap enough to justify a forced scavenge,
// so the call runs at most once per freeOSMemoryInterval.
func (m *TuiModel) maybeFreeOSMemory() {
	now := time.Now()
	if !m.lastFreeOSMemory.IsZero() && now.Sub(m.lastFreeOSMemory) < freeOSMemoryInterval {
		return
	}
	m.lastFreeOSMemory = now
	releaseOSMemory()
}

// Memory bounds for the TUI transcript.
//
// The TUI keeps every block ever shown in m.blocks so the user can scroll back
// through the whole conversation. Rendered blocks hold their content,
// cachedLines (split rendered ANSI), and (for tools) output — kilobytes per
// block, megabytes for a chatty tool. A long session grows the heap without
// bound unless we bound the resident set.
//
// Two layers of bounds:
//
//  1. Scrollback disk cache (tui_scrollback.go): only a ~256 KiB window of the
//     most recent rendered blocks stays resident; older blocks are flushed to a
//     temp file (content/cachedLines/output dropped to nil, lineCount kept) and
//     paged back in on scroll-up/resize. History is never dropped — it's paged
//     out. See tui_scrollback.go.
//
//  2. Per-field caps below: bound the worst single offenders so even a single
//     tool call can't blow the budget before eviction runs.
//
// These constants cap the per-field worst case:
//   - tuiMaxToolOutput:  per-tool cap on the raw .output buffer (the source
//     shown in the click-to-expand modal). Tool output (e.g. bash printing a
//     50MB log) is the biggest single offender; we keep a tail slice so the
//     modal still shows the end.
//
// Subagent streaming output goes through the same per-block .output buffer (the
// modal renders that block), so tuiMaxToolOutput bounds a runaway child agent
// too — there is no separate, globally shared modal accumulator.
const (
	tuiMaxToolOutput = 1 << 20 // 1 MiB per tool block .output
)

// capToolOutput trims a tool block's raw output buffer to its last maxBytes,
// preserving the trailing content (what the modal shows when expanded). This
// bounds the memory of tools that emit huge outputs (e.g. bash cat-ing a log).
func capToolOutput(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	// Keep the tail; trim at a line boundary if possible so the modal doesn't
	// start mid-line.
	tail := s[len(s)-maxBytes:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < maxBytes/2 {
		tail = tail[i+1:]
	}
	return tail
}
