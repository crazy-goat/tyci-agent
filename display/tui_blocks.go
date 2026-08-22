package display

import (
	"strings"
	"time"

	"github.com/decodo/tyci/stream"
)

func (m *TuiModel) handleBlockMsg(msg tuiMsgBlock) {
	switch msg.kind {
	case "thinking", "text", "tool-start", "tool-end", "block", "error":
		periodicFreeOSMemory(&m.blockEventCount)
	}
	// One choke point for every kind of untrusted text, because there is no
	// kind that is trusted: model output, tool output and streamed progress
	// are all arbitrary bytes drawn into a terminal that reads some of those
	// bytes as commands. See tui_sanitize.go for what a raw ESC or CR does to
	// the frame. Done here rather than at render time, when the TUI's own
	// colour codes share the same strings.
	switch msg.kind {
	case "thinking", "text", "tool-delta", "tool-end", "tool-progress", "block", "error":
		msg.content = sanitizeUntrusted(msg.content)
	}
	switch msg.kind {
	case "request-start":
		// Reset the elapsed-time counter at the start of each API turn so the
		// status bar shows per-turn wall time instead of accumulating from
		// the user's initial submit. See issue #83.
		m.requestStartTime = time.Now()
	case "thinking":
		m.status = "thinking"
		m.appendOrAppend("thinking", msg.content)
	case "text":
		if m.status != "responding" {
			m.status = "responding"
		}
		m.appendOrAppend("text", msg.content)
	case "tool-start":
		// New tool block → force-render previous dirty blocks (thinking/text)
		m.forceRenderDirtyBlocks()
		m.status = "tool"
		idx := len(m.blocks)
		m.blocks = append(m.blocks, block{
			kind: "tool", toolName: msg.toolName,
			toolState: "running", collapsed: true,
			maxLines:  defaultMaxLines(msg.toolName),
			startTime: time.Now(),
		})
		m.toolQueue = append(m.toolQueue, idx)
		// Appending a block never changes how earlier blocks render (width is
		// unchanged), so only the total-line cache needs invalidation.
		m.invalidateTotalLines()
		// Bound memory: flush old blocks' heavy fields (content/cachedLines/
		// output) to the scrollback cache file, keeping only a ~250KB window
		// of rendered lines resident. Block indices and the tool queue stay
		// stable — the cache is paged back in on scroll-up/resize. See
		// tui_scrollback.go.
		m.maybeFlushOldBlocks()

		// Track the subagent's queue index for arg-delta routing. Nothing about
		// the modal is reset here: a new subagent must not wipe the output the
		// user is currently reading — each block keeps its own buffer.
		if msg.toolName == "subagent" {
			m.subagentToolIdx = len(m.toolQueue) - 1
		}
	case "tool-delta":
		// For subagent: keep raw JSON args in the inline block for summary
		// rendering; streamed progress lands in the block's .output instead.
		if m.subagentToolIdx >= 0 && msg.content != "" && len(m.toolQueue) > m.subagentToolIdx {
			bidx := m.toolQueue[m.subagentToolIdx]
			if bidx >= 0 && bidx < len(m.blocks) && m.blocks[bidx].kind == "tool" && m.blocks[bidx].toolName == "subagent" {
				m.blocks[bidx].content += msg.content
				// The collapsed tool line only changes once the args JSON is
				// complete (formatToolCall shows "tool(...)" until it parses),
				// so skip cache invalidation and parse attempts for deltas
				// that cannot end the JSON document.
				if jsonMaybeComplete(m.blocks[bidx].content) {
					m.blocks[bidx].cachedLines = nil
					m.blocks[bidx].cachedLineCount = 0
					delete(m.toolDisplayCache, bidx)
					m.invalidateTotalLines()

					// Only the heading of the block on screen can change; every
					// other block's title is derived when it is opened.
					if m.subagentModalBlockIdx == bidx {
						m.subagentModalTitle = m.modalTitleForBlock(bidx)
					}
				}
				break
			}
		}
		m.appendToLastTool(msg.content)
	case "tool-end":
		// tool-end arrives in queue order, so the finishing tool is the block at
		// the front of the queue. Verify it really is a subagent instead of
		// trusting the tracked queue index alone.
		frontIdx := -1
		if len(m.toolQueue) > 0 {
			frontIdx = m.toolQueue[0]
		}
		isSubagentEnd := frontIdx >= 0 && frontIdx < len(m.blocks) &&
			m.blocks[frontIdx].kind == "tool" && m.blocks[frontIdx].toolName == "subagent"

		if isSubagentEnd {
			// For subagent: pop queue entry without appending result to block content
			m.toolQueue = m.toolQueue[1:]
			m.blocks[frontIdx].toolState = "done"
			m.blocks[frontIdx].duration = toolDuration(msg.duration, m.blocks[frontIdx].startTime)
			m.blocks[frontIdx].cachedLines = nil
			delete(m.toolDisplayCache, frontIdx)
			m.invalidateTotalLines()
			if m.subagentToolIdx == 0 {
				m.subagentToolIdx = -1
			} else if m.subagentToolIdx > 0 {
				m.subagentToolIdx--
			}
		} else {
			m.finishToolAt(msg.content, msg.duration)
			// If subagent is deeper in queue, decrement its index
			if m.subagentToolIdx > 0 {
				m.subagentToolIdx--
			}
		}
		// "done" is a property of the block on screen, not of whichever tool
		// happened to finish last.
		if frontIdx >= 0 && m.subagentModalBlockIdx == frontIdx {
			m.subagentModalDone = true
		}
	case "tool-progress":
		// Progress for every tool (subagent included) accumulates in its own
		// block, capped per block by appendTool. The modal just looks at it.
		bidx := -1
		if msg.toolIdx >= 0 && msg.toolIdx < len(m.toolQueue) {
			bidx = m.toolQueue[msg.toolIdx]
		}
		if m.subagentModalActive && bidx >= 0 && bidx == m.subagentModalBlockIdx {
			before := m.subagentModalLineCount()
			m.appendTool(msg.toolIdx, msg.content)
			// Keep a manually scrolled viewport anchored on the same text
			// instead of letting growing output slide it towards the newest
			// lines; the cap trimming the top shows up as a negative delta.
			if m.subagentModalScroll > 0 {
				m.subagentModalScroll += m.subagentModalLineCount() - before
			}
			m.clampSubagentModalScroll()
		} else {
			m.appendTool(msg.toolIdx, msg.content)
		}
	case "usage":
		m.lastUsage = msg.usage
		m.lastStats = msg.stats
		// Usage info is already shown in the bottom status bar (buildStatus).
		// No need to add a separate block in the conversation.
	case "error":
		// New error block → force-render previous dirty blocks
		m.forceRenderDirtyBlocks()
		idx := len(m.blocks)
		m.blocks = append(m.blocks, block{kind: "error", content: msg.content, dirty: true})
		m.dirtyBlocks[idx] = true
		m.invalidateTotalLines()
		m.maybeFlushOldBlocks()
	case "done":
		m.status = "idle"
		m.reading = true
		m.requestStartTime = time.Time{}
		// Force-render all dirty blocks now that streaming is complete
		m.forceRenderDirtyBlocks()
	case "block":
		// New info block → force-render previous dirty blocks
		m.forceRenderDirtyBlocks()
		idx := len(m.blocks)
		m.blocks = append(m.blocks, block{kind: "block", content: msg.content, dirty: true})
		m.dirtyBlocks[idx] = true
		m.invalidateTotalLines()
		m.maybeFlushOldBlocks()
	case "set-model":
		m.modelName = msg.content
	case "reset":
		m.blocks = nil
		m.scrollLine = 0
		m.atBottom = true
		m.reading = true
		m.status = "idle"
		m.requestStartTime = time.Time{}
		m.lastUsage = stream.Usage{}
		m.lastStats = stream.Stats{}
		m.dirtyBlocks = make(map[int]bool)
		m.mdCacheRendered = make(map[int]string)
		m.streamWraps = make(map[int]*streamWrap)
		m.toolDisplayCache = make(map[int]string)
		m.cachedTotalLines = -1
		m.invalidateMessageRegion()
		m.subagentModalActive = false
		m.subagentModalBlockIdx = -1
		m.subagentModalScroll = 0
		m.subagentToolIdx = -1
		// Issue #88: /new also drops any pending user messages queued
		// while a request was in flight. The new conversation starts
		// from a clean slate, with no carried-over follow-ups.
		m.clearMessageQueue()
		// Reset scrollback cache: a fresh conversation has no history to page.
		m.scrollback.reset()
	case "queue-drained":
		// Issue #88: the agent loop drained the pending-message queue
		// and is about to deliver the lines to the model. The on-screen
		// queue panel must clear now so the user knows the messages
		// have been picked up, and each drained line becomes a "You: …"
		// block in the transcript — at the moment the model actually
		// sees the message, not when the user typed it. This keeps the
		// user's view of the conversation aligned with what the model
		// has processed so far.
		m.forceRenderDirtyBlocks()
		for _, line := range msg.queuedLines {
			m.blocks = append(m.blocks, block{kind: "user", content: "You: " + line, dirty: true})
			m.dirtyBlocks[len(m.blocks)-1] = true
		}
		// Rebuild the snapshot from whatever is left on the channel:
		// lines the user typed between the agent's drain and this
		// handler running stay visible.
		m.queueItems = nil
		if m.queue != nil {
			for {
				select {
				case s := <-m.queue:
					m.queueItems = append(m.queueItems, s)
				default:
					m.invalidateTotalLines()
					return
				}
			}
		}
		m.invalidateTotalLines()
	}

	// If user is at bottom, keep scrolled to bottom when new content arrives
	if m.atBottom {
		m.scrollLine = 0
	}
	m.clampScroll()
}

// newContentBlock builds a thinking/text/user block for appendOrAppend. A
// thinking block additionally starts its own clock here — block.duration is
// only ever set by tool-end, so a thinking block has to time itself, from
// this moment until forceRenderDirtyBlocks freezes it — and gets a first
// attempt at its collapsed-line summary, covering the case where the whole
// block arrives as one chunk instead of via streaming deltas.
func newContentBlock(kind, content string) block {
	b := block{kind: kind, content: content, dirty: true}
	if kind == "thinking" {
		b.collapsed = true
		b.startTime = time.Now()
		freezeThinkingSummary(&b, false)
	}
	return b
}

func (m *TuiModel) appendOrAppend(kind, content string) {
	if len(m.blocks) == 0 {
		m.invalidateTotalLines()
		idx := 0
		m.blocks = append(m.blocks, newContentBlock(kind, content))
		m.dirtyBlocks[idx] = true
		return
	}
	last := &m.blocks[len(m.blocks)-1]
	if last.kind == kind {
		// Don't merge user messages ("You: ...") with agent response text
		// or vice versa. Without this check, consecutive user and agent text
		// blocks get concatenated, corrupting conversation display.
		lastIsUser := strings.HasPrefix(last.content, "You: ")
		newIsUser := strings.HasPrefix(content, "You: ")
		if lastIsUser != newIsUser {
			// Different sources → create new block
			m.invalidateTotalLines()
			m.forceRenderDirtyBlocks()
			idx := len(m.blocks)
			m.blocks = append(m.blocks, newContentBlock(kind, content))
			m.dirtyBlocks[idx] = true
			m.maybeFlushOldBlocks()
			return
		}
		// ── Streaming hot path (issue: CPU grows with context length) ──
		// This runs on every streamed token. The message region cache must be
		// invalidated (the last block's content changed), but we deliberately
		// avoid invalidateTotalLines() here: that sets cachedTotalLines = -1,
		// forcing totalRenderedLines() to re-sum EVERY block on the next frame.
		// That scan is O(total blocks) and, over a long conversation, becomes
		// the dominant per-token cost — CPU climbs from ~1% to 10-20% as the
		// transcript grows. Instead we update the cached total incrementally:
		// only the last block's line count changes, so we recompute just that
		// block and adjust the running total by the delta. See #84 follow-up.
		m.invalidateMessageRegion()
		// Old contribution of this block. Use the cached count when present;
		// otherwise render it now so the delta below is exact (cachedLineCount
		// can be 0 for a not-yet-rendered block that nonetheless has lines).
		oldCount := last.cachedLineCount
		if oldCount == 0 && m.cachedTotalLines >= 0 {
			oldCount = len(m.getBlockLines(len(m.blocks)-1, false))
			last = &m.blocks[len(m.blocks)-1] // getBlockLines may have grown the slice header; refresh
		}
		last.content += content
		if kind == "thinking" {
			freezeThinkingSummary(last, false)
		}
		last.dirty = true
		last.cachedLineCount = 0
		last.cachedLines = nil
		idx := len(m.blocks) - 1
		m.dirtyBlocks[idx] = true
		// Keep m.streamWraps[idx]: it detects the append itself and re-wraps
		// only the last logical line instead of the whole block.
		delete(m.mdCacheRendered, idx)
		// Incrementally fix cachedTotalLines: the last block never carries a
		// trailing spacer (totalRenderedLines strips it), so it contributes
		// exactly its own line count. Recompute just this block and apply the
		// delta. If the total wasn't cached, leave it (-1) so it's computed
		// fresh on demand.
		if m.cachedTotalLines >= 0 {
			newLines := m.getBlockLines(idx, false)
			m.cachedTotalLines += len(newLines) - oldCount
			if m.cachedTotalLines < 0 {
				m.cachedTotalLines = 0
			}
		}
		return
	}
	m.invalidateTotalLines()
	// New block type starting → force-render all dirty blocks immediately
	// so they show final markdown before the new block appears.
	m.forceRenderDirtyBlocks()
	idx := len(m.blocks)
	m.blocks = append(m.blocks, newContentBlock(kind, content))
	m.dirtyBlocks[idx] = true
	// Adding a block only shifts line offsets; earlier blocks render the same.
	m.invalidateTotalLines()
	m.maybeFlushOldBlocks()
}

// jsonMaybeComplete reports whether s could be a complete JSON document, used
// to skip parse attempts on partially-streamed tool arguments.
func jsonMaybeComplete(s string) bool {
	return strings.HasSuffix(strings.TrimRight(s, " \t\r\n"), "}")
}

// ─── Glamour renderer cache ─────────────────────────────────────────
