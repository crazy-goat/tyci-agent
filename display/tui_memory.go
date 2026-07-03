package display

import (
	"strings"
)

// Memory bounds for the TUI transcript.
//
// The TUI keeps every block ever shown in m.blocks so the user can scroll back
// through the whole conversation. Without a cap, a long coding session (thousands
// of tool calls, each with full file contents in .output) grows the heap without
// bound: the slices in m.blocks and the per-block index maps
// (mdCacheRendered/toolDisplayCache/streamWraps) all keep references to evicted
// content, so the GC can never reclaim it.
//
// These constants bound the worst-case resident set:
//   - tuiMaxBlocks:      hard cap on the number of retained blocks. When exceeded,
//     oldest blocks are dropped and their cache entries freed.
//     The default (tuiMaxHistory=500) holds a long conversation
//     while keeping a single block's worth of cache maps small.
//   - tuiMaxToolOutput:  per-tool cap on the raw .output buffer (the source shown
//     in the click-to-expand modal). Tool output (e.g. bash
//     printing a 50MB log) is the biggest single offender; we
//     keep a tail slice so the modal still shows the end.
//   - tuiMaxModalBuffer: cap on the subagent modal streaming buffer. A misbehaving
//     child agent streaming forever would otherwise keep the
//     builder growing until the modal is closed.
const (
	tuiMaxToolOutput  = 1 << 20 // 1 MiB per tool block .output
	tuiMaxModalBuffer = 1 << 20 // 1 MiB for the subagent modal accumulator
)

// compactBlocks drops the oldest blocks (and their render caches) so that
// len(m.blocks) <= limit. Block indices shift down by the number dropped, so
// every index-keyed map is rebuilt with the new keys; this also drops entries
// for evicted blocks, freeing their cached strings.
//
// Call this after appending a new block (handleBlockMsg "tool-start"/"usage"/
// "error"/"block"/"reset"-free paths). It is O(n) but runs only when the cap is
// exceeded, and n is bounded by limit, so it is cheap and does not run on the
// streaming hot path.
func (m *TuiModel) compactBlocks(limit int) {
	if limit <= 0 || len(m.blocks) <= limit {
		return
	}
	drop := len(m.blocks) - limit
	if drop <= 0 {
		return
	}

	// Drop the subagent modal reference if it points into the evicted range.
	// The modal is closed/reset by the caller when its tool ends; this only
	// guards against a still-open modal whose backing block scrolled off.
	if m.subagentModalToolIdx >= 0 {
		// Find the block index the modal is bound to.
		modalBlockIdx := -1
		if m.subagentModalToolIdx < len(m.toolQueue) {
			modalBlockIdx = m.toolQueue[m.subagentModalToolIdx]
		}
		if modalBlockIdx >= 0 && modalBlockIdx < drop {
			// Backing block evicted — close the modal to release its buffer.
			m.subagentModalActive = false
			m.subagentModalContent.Reset()
			m.subagentModalToolIdx = -1
			m.subagentModalDone = false
		}
	}

	// Shift blocks down.
	m.blocks = append(m.blocks[:0], m.blocks[drop:]...)

	// Reindex the tool queue (block indices shift by -drop; drop entries < 0).
	if len(m.toolQueue) > 0 {
		nq := make([]int, 0, len(m.toolQueue))
		for _, idx := range m.toolQueue {
			idx -= drop
			if idx >= 0 {
				nq = append(nq, idx)
			}
		}
		m.toolQueue = nq
	}
	// toolQueue indices don't shift (they're positions in the queue, not block
	// indices); if the modal's queue entry was dropped above we already reset it.

	// Rebuild index-keyed maps with shifted keys, dropping evicted entries.
	m.dirtyBlocks = reindexBoolMap(m.dirtyBlocks, drop)
	m.mdCacheRendered = reindexStringMap(m.mdCacheRendered, drop)
	m.streamWraps = reindexStreamWrapMap(m.streamWraps, drop)
	m.toolDisplayCache = reindexStringMap(m.toolDisplayCache, drop)

	// Line counts are now stale (block identities shifted); force recompute.
	m.invalidateTotalLines()
	m.clampScroll()
}

// reindexBoolMap returns a new map with keys shifted by -drop; keys < drop are
// dropped (their blocks were evicted).
func reindexBoolMap(in map[int]bool, drop int) map[int]bool {
	if len(in) == 0 {
		return in
	}
	out := make(map[int]bool, len(in))
	for k, v := range in {
		if k >= drop {
			out[k-drop] = v
		}
	}
	return out
}

// reindexStringMap shifts string-valued index maps.
func reindexStringMap(in map[int]string, drop int) map[int]string {
	if len(in) == 0 {
		return in
	}
	out := make(map[int]string, len(in))
	for k, v := range in {
		if k >= drop {
			out[k-drop] = v
		}
	}
	return out
}

// reindexStreamWrapMap shifts the streamWrap index map.
func reindexStreamWrapMap(in map[int]*streamWrap, drop int) map[int]*streamWrap {
	if len(in) == 0 {
		return in
	}
	out := make(map[int]*streamWrap, len(in))
	for k, v := range in {
		if k >= drop {
			out[k-drop] = v
		}
	}
	return out
}

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
