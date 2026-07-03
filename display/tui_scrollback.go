package display

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// Scrollback disk cache for the TUI transcript.
//
// Problem: the TUI keeps every block ever shown in m.blocks so the user can
// scroll back through the whole conversation. Rendered blocks hold their
// content, cachedLines (split rendered ANSI), and (for tools) output — easily
// kilobytes per block, megabytes for a chatty bash call. A long coding session
// with thousands of tool calls grows the heap without bound.
//
// Solution: keep only a ~tuiScrollbackResidentBudget window of the most recent
// rendered blocks fully resident in RAM. Older blocks are *flushed*: their
// heavy fields (content, cachedLines, output) are written to an append-only
// temp file and the in-memory copies dropped to nil. The block struct stays
// (indices, kind, toolName, lineCount are all stable) so scroll math, the tool
// queue, and the cache maps keep working unchanged. When the viewport scrolls
// up into a flushed block, ensureBlockResident pages its rendered lines back in
// from the file; the resident window then evicts a different block to stay
// within budget. Resize re-pages affected blocks and re-wraps them.
//
// The cache is process-local and lives in a temp file deleted on Close (called
// from the TUI shutdown path). It never holds the only copy of a block — the
// raw content of a flushed block is in the session file (if recording) and the
// rendered form is in the cache file, so a crash loses nothing the user can't
// reconstruct by re-running the session.

// tuiScrollbackResidentBudget is the soft cap on the bytes of rendered-line
// data (cachedLines string content) kept in RAM. Older blocks beyond the
// resident window are flushed to disk. ~256 KiB holds a few screens of
// scrollback for instant scrolling; everything older pages in on demand.
const tuiScrollbackResidentBudget = 256 * 1024

// scrollbackCache manages the temp file + resident window accounting.
//
// Concurrency: all access is from the bubbletea event-loop goroutine (Update /
// View), so no locking is needed for the model fields. The file itself is only
// touched from that goroutine too. The mutex exists solely to guard Close
// against the finalizer/shutdown path which may run from a different goroutine.
type scrollbackCache struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	openErr  error

	// residentBytes is the sum of len(cachedLines[i]) over all resident (non-
	// flushed) blocks. Updated by flush/ensure so maybeFlushOldBlocks knows
	// when to evict. Recomputed from scratch in maybeFlushOldBlocks (the only
	// read site) so it doesn't go stale when forceRenderDirtyBlocks populates
	// cachedLines without going through the scrollback helpers.
	residentBytes int
}

// ensureOpen lazily creates the temp cache file. It is a no-op if already open
// or if a previous open failed (we degrade gracefully to no caching: blocks
// stay resident, memory is unbounded but correct).
func (sc *scrollbackCache) ensureOpen() error {
	if sc.openErr != nil {
		return sc.openErr
	}
	if sc.file != nil {
		return nil
	}
	f, err := os.CreateTemp("", "tyci-scrollback-*.bin")
	if err != nil {
		sc.openErr = err
		return err
	}
	sc.file = f
	sc.filePath = f.Name()
	return nil
}

// flushBlock writes a block's rendered lines to the cache file and records the
// byte range, then drops the in-memory copies (content/cachedLines/output) so
// the GC can reclaim them. The block keeps cachedLineCount (needed for scroll
// math) and gets flushed=true + fileOffset/fileBytes + flushedWidth for later
// page-in (width is stored so a resize can detect stale wrapping and re-wrap).
//
// The encoded form is: 4-byte big-endian line count, then each line as a
// 4-byte big-endian length prefix followed by the line's bytes. This is trivial
// to page-in for an arbitrary line range without scanning the whole block.
func (sc *scrollbackCache) flushBlock(b *block, width int) {
	if b.flushed || b.cachedLines == nil {
		return
	}
	if err := sc.ensureOpen(); err != nil {
		return // degrade: keep resident
	}
	// Flush any buffered writes so the seek offset is accurate, then position
	// at end of file for the append.
	off, err := sc.file.Seek(0, io.SeekEnd)
	if err != nil {
		return
	}

	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b.cachedLines)))
	if _, err := sc.file.Write(hdr[:]); err != nil {
		return
	}
	written := 4
	for _, line := range b.cachedLines {
		var ln [4]byte
		binary.BigEndian.PutUint32(ln[:], uint32(len(line)))
		if _, err := sc.file.Write(ln[:]); err != nil {
			return
		}
		n, err := sc.file.Write([]byte(line))
		if err != nil {
			return
		}
		written += 4 + n
	}
	b.fileOffset = off
	b.fileBytes = written
	b.flushedWidth = width
	b.flushed = true
	// Drop heavy in-memory fields.
	sc.residentBytes -= blockLinesBytes(b.cachedLines)
	b.cachedLines = nil
	b.content = ""
	b.output = ""
}

// pageIn reads a flushed block's rendered lines back from the cache file and
// restores them to b.cachedLines. Returns nil if the block isn't flushed or the
// read fails (caller should treat as empty/lost — shouldn't happen in practice).
func (sc *scrollbackCache) pageIn(b *block) []string {
	if !b.flushed || sc.file == nil {
		return nil
	}
	_, err := sc.file.Seek(b.fileOffset, io.SeekStart)
	if err != nil {
		return nil
	}
	r := bufio.NewReader(io.LimitReader(sc.file, int64(b.fileBytes)))
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil
	}
	n := int(binary.BigEndian.Uint32(hdr[:]))
	if n < 0 || n > 1<<20 { // sanity cap
		return nil
	}
	lines := make([]string, n)
	for i := 0; i < n; i++ {
		var ln [4]byte
		if _, err := io.ReadFull(r, ln[:]); err != nil {
			return nil
		}
		l := int(binary.BigEndian.Uint32(ln[:]))
		if l < 0 || l > 1<<20 {
			return nil
		}
		s := make([]byte, l)
		if _, err := io.ReadFull(r, s); err != nil {
			return nil
		}
		lines[i] = string(s)
	}
	b.cachedLines = lines
	b.flushed = false
	sc.residentBytes += blockLinesBytes(lines)
	return lines
}

// reset clears all cache state (used on /new). Closes the file; a new one is
// created lazily on the next flush.
func (sc *scrollbackCache) reset() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.file != nil {
		_ = sc.file.Close()
		_ = os.Remove(sc.filePath)
		sc.file = nil
		sc.filePath = ""
	}
	sc.residentBytes = 0
	sc.openErr = nil
}

// close releases the cache file. Safe to call from any goroutine (the shutdown
// path). After close, further flushes degrade to no-op (blocks stay resident).
func (sc *scrollbackCache) close() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.file != nil {
		_ = sc.file.Close()
		_ = os.Remove(sc.filePath)
		sc.file = nil
		sc.filePath = ""
	}
}

// dropResidentCaches releases the per-block render caches for a block that has
// just been flushed to the scrollback file.
//
// flushBlock drops the block's heavy fields (content/cachedLines/output) but it
// only touches the block struct — it can't reach the model's cache maps. Those
// maps mirror the same rendered bytes:
//   - mdCacheRendered[idx] holds the full rendered ANSI for every text/thinking/
//     error/block block — a near-exact duplicate of cachedLines (joined vs
//     split). Left in place, it keeps a complete copy of every flushed block's
//     rendered output resident forever, so the scrollback budget frees only ~half
//     the memory and the map itself grows without bound over a long session.
//   - toolDisplayCache[idx] / streamWraps[idx] are smaller per-block caches with
//     the same lifetime problem.
//
// The display path for a flushed block pages its lines back from disk
// (getBlockLines→ensureBlockResident) and never consults these maps, so dropping
// them is safe; they are rebuilt lazily if the block is paged in and re-rendered.
func (m *TuiModel) dropResidentCaches(idx int) {
	delete(m.mdCacheRendered, idx)
	delete(m.toolDisplayCache, idx)
	delete(m.streamWraps, idx)
}

// blockLinesBytes returns the total byte length of a slice of line strings.
func blockLinesBytes(lines []string) int {
	n := 0
	for _, l := range lines {
		n += len(l)
	}
	return n
}

// maybeFlushOldBlocks is called after a new block is appended. It walks the
// oldest resident blocks and flushes them to the scrollback file until the
// resident window is within budget. The newest blocks (and any block currently
// on the tool queue, since it may still receive deltas) are never flushed.
//
// This is the only eviction trigger. It runs O(flushed) but only when the
// budget is exceeded, and the per-block work is a single sequential file write
// of already-rendered lines — no re-rendering.
func (m *TuiModel) maybeFlushOldBlocks() {
	// Recompute resident bytes from scratch: cachedLines may have been populated
	// by forceRenderDirtyBlocks or getBlockLines without updating the counter,
	// and this is the one place that needs an accurate total. O(blocks) but
	// only runs when we're over budget, so the cost is amortized.
	m.scrollback.residentBytes = m.residentBlockBytes()
	if m.scrollback.residentBytes <= tuiScrollbackResidentBudget {
		return
	}
	// Don't flush blocks still on the tool queue (may receive deltas/results).
	queued := make(map[int]bool, len(m.toolQueue))
	for _, idx := range m.toolQueue {
		queued[idx] = true
	}
	// Walk oldest-first; stop once under budget or when we'd flush a queued/
	// dirty/actively-streaming block (those must stay resident).
	for i := range m.blocks {
		if m.scrollback.residentBytes <= tuiScrollbackResidentBudget {
			return
		}
		b := &m.blocks[i]
		if b.flushed || b.dirty || queued[i] {
			continue
		}
		if b.cachedLines == nil {
			// Nothing resident to flush (e.g. never rendered); skip.
			continue
		}
		m.scrollback.flushBlock(b, m.width)
		m.dropResidentCaches(i)
	}
}

// ensureBlockResident makes block idx's rendered lines available in memory,
// paging them in from the scrollback cache if the block was flushed. After this
// call, b.cachedLines is non-nil (or the block genuinely has no content).
//
// If paging in pushes the resident window over budget, an older resident block
// is flushed to make room. This is the on-demand path triggered by scrolling up
// or a resize touching flushed blocks.
func (m *TuiModel) ensureBlockResident(idx int) []string {
	if idx < 0 || idx >= len(m.blocks) {
		return nil
	}
	b := &m.blocks[idx]
	if b.cachedLines != nil {
		return b.cachedLines
	}
	if !b.flushed {
		// Not flushed and no cached lines: render it (e.g. a block that was
		// never viewed). This populates cachedLines.
		return m.getBlockLines(idx, false)
	}
	lines := m.scrollback.pageIn(b)
	if lines == nil {
		// Page-in failed (cache file gone/corrupt): mark not-flushed so we
		// don't retry forever; the block renders as empty.
		b.flushed = false
		b.cachedLineCount = 0
		return nil
	}
	// If the terminal width changed since the block was flushed, the paged-in
	// lines are wrapped for the old width — re-wrap them for the current width
	// before returning. This keeps old scrollback readable after a resize
	// without re-running the (discarded) markdown renderer.
	if b.flushedWidth != 0 && b.flushedWidth != m.width {
		lines = rewrapLines(lines, b.kind, m.width)
		b.cachedLines = lines
		b.cachedLineCount = len(lines)
		m.scrollback.residentBytes = m.residentBlockBytes()
	}
	// Evict an older resident block to stay near budget. Don't evict queued
	// or the block we just paged in.
	if m.scrollback.residentBytes > tuiScrollbackResidentBudget {
		queued := make(map[int]bool, len(m.toolQueue))
		for _, q := range m.toolQueue {
			queued[q] = true
		}
		for j := 0; j < idx; j++ {
			if m.scrollback.residentBytes <= tuiScrollbackResidentBudget {
				break
			}
			bj := &m.blocks[j]
			if bj.flushed || bj.dirty || queued[j] || bj.cachedLines == nil {
				continue
			}
			m.scrollback.flushBlock(bj, m.width)
			m.dropResidentCaches(j)
		}
	}
	return lines
}

// residentBlockBytes is a test helper returning the bytes of rendered-line
// content currently held in RAM across all blocks.
func (m *TuiModel) residentBlockBytes() int {
	n := 0
	for i := range m.blocks {
		if m.blocks[i].cachedLines != nil {
			n += blockLinesBytes(m.blocks[i].cachedLines)
		}
	}
	return n
}

// rewrapLines re-wraps already-rendered (ANSI-styled) lines for a new terminal
// width. Used after paging a flushed block back in when the width has changed
// since it was flushed: the stored lines are wrapped for the old width, so we
// re-flow them to the current width without re-running the markdown renderer
// (the styled text is preserved; only the soft-wrap breaks move).
//
// Each input line is treated as a logical line: if it fits within width it's
// kept as-is, otherwise it's hard-wrapped via wrapText (which preserves ANSI
// codes). Blank lines and spacers pass through unchanged.
func rewrapLines(lines []string, kind string, width int) []string {
	if width <= 0 || len(lines) == 0 {
		return lines
	}
	maxW := width
	if kind == "thinking" || kind == "error" || kind == "block" {
		maxW = width - 2 // these blocks render with a leading bar + space
	}
	if maxW < 10 {
		maxW = 10
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			out = append(out, "")
			continue
		}
		// Only re-wrap if the line's visible width exceeds the new max.
		if lipgloss.Width(line) <= maxW {
			out = append(out, line)
			continue
		}
		wrapped := wrapText(line, maxW, 0)
		for _, wl := range strings.Split(wrapped, "\n") {
			wl = strings.TrimSuffix(wl, clearLine)
			out = append(out, wl)
		}
	}
	return out
}
