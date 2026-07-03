package display

import "strings"

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
//   - tuiMaxModalBuffer: cap on the subagent modal streaming buffer. A
//     misbehaving child agent streaming forever would otherwise keep the
//     builder growing until the modal is closed.
const (
	tuiMaxToolOutput  = 1 << 20 // 1 MiB per tool block .output
	tuiMaxModalBuffer = 1 << 20 // 1 MiB for the subagent modal accumulator
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

// capModalBuffer trims the subagent modal accumulator to its last maxBytes,
// keeping the tail (the most recent streaming output, which is what the user
// sees when the modal is pinned to the bottom).
func capModalBuffer(b *strings.Builder, maxBytes int) {
	if b == nil || maxBytes <= 0 {
		return
	}
	if b.Len() <= maxBytes {
		return
	}
	s := b.String()
	tail := s[len(s)-maxBytes:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < maxBytes/2 {
		tail = tail[i+1:]
	}
	b.Reset()
	b.WriteString(tail)
}
