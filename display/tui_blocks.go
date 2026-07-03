package display

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/decodo/tyci/stream"
)

func (m *TuiModel) handleBlockMsg(msg tuiMsgBlock) {
	switch msg.kind {
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

		// Track subagent tool index for modal (but don't auto-open).
		// Modal opens on user click on the subagent block.
		if msg.toolName == "subagent" {
			m.subagentModalToolIdx = len(m.toolQueue) - 1
			m.subagentModalContent.Reset()
			m.subagentModalScroll = 0
			m.subagentModalDone = false
			m.subagentModalTitle = "subagent"
		}
	case "tool-delta":
		// For subagent: keep raw JSON args in the inline block for summary rendering,
		// while progress/output goes only to the modal.
		if m.subagentModalToolIdx >= 0 && msg.content != "" && len(m.toolQueue) > m.subagentModalToolIdx {
			bidx := m.toolQueue[m.subagentModalToolIdx]
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

					var args map[string]any
					if json.Unmarshal([]byte(m.blocks[bidx].content), &args) == nil {
						if title := subagentTitleFromArgs(args); title != "" {
							m.subagentModalTitle = truncateString(title, 80)
						}
					}
				}
				break
			}
		}
		m.appendToLastTool(msg.content)
	case "tool-end":
		// Determine if this tool-end is for the subagent
		isSubagentEnd := m.subagentModalToolIdx == 0 && m.subagentModalToolIdx >= 0 && len(m.toolQueue) > 0

		if isSubagentEnd {
			// For subagent: pop queue entry without appending result to block content
			if len(m.toolQueue) > 0 {
				idx := m.toolQueue[0]
				m.toolQueue = m.toolQueue[1:]
				if idx >= 0 && idx < len(m.blocks) && m.blocks[idx].kind == "tool" {
					m.blocks[idx].toolState = "done"
					m.blocks[idx].duration = time.Since(m.blocks[idx].startTime)
					m.blocks[idx].cachedLines = nil
					delete(m.toolDisplayCache, idx)
				}
			}
			m.subagentModalDone = true
			m.subagentModalToolIdx = -1
		} else {
			m.finishToolAt(msg.content)
			// If subagent is deeper in queue, decrement its index
			if m.subagentModalToolIdx > 0 {
				m.subagentModalToolIdx--
			}
		}
	case "tool-progress":
		// Subagent progress captured for modal (even if not active), never to inline block
		if msg.toolIdx == m.subagentModalToolIdx {
			m.subagentModalContent.WriteString(msg.content)
			// Bound the modal accumulator so a runaway child agent can't grow
			// the buffer past tuiMaxModalBuffer. Keep the tail (most recent).
			capModalBuffer(m.subagentModalContent, tuiMaxModalBuffer)
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
		m.lastUsage = stream.Usage{}
		m.lastStats = stream.Stats{}
		m.dirtyBlocks = make(map[int]bool)
		m.mdCacheRendered = make(map[int]string)
		m.streamWraps = make(map[int]*streamWrap)
		m.toolDisplayCache = make(map[int]string)
		m.cachedTotalLines = -1
		m.subagentModalActive = false
		m.subagentModalContent.Reset()
		// Reset scrollback cache: a fresh conversation has no history to page.
		m.scrollback.reset()
	}

	// If user is at bottom, keep scrolled to bottom when new content arrives
	if m.atBottom {
		m.scrollLine = 0
	}
	m.clampScroll()
}

func (m *TuiModel) appendOrAppend(kind, content string) {
	m.invalidateTotalLines()
	if len(m.blocks) == 0 {
		idx := 0
		m.blocks = append(m.blocks, block{kind: kind, content: content, dirty: true})
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
			m.forceRenderDirtyBlocks()
			idx := len(m.blocks)
			m.blocks = append(m.blocks, block{kind: kind, content: content, dirty: true})
			m.dirtyBlocks[idx] = true
			m.maybeFlushOldBlocks()
			return
		}
		last.content += content
		last.dirty = true
		last.cachedLineCount = 0
		last.cachedLines = nil
		idx := len(m.blocks) - 1
		m.dirtyBlocks[idx] = true
		// Keep m.streamWraps[idx]: it detects the append itself and re-wraps
		// only the last logical line instead of the whole block.
		delete(m.mdCacheRendered, idx)
		return
	}
	// New block type starting → force-render all dirty blocks immediately
	// so they show final markdown before the new block appears.
	m.forceRenderDirtyBlocks()
	idx := len(m.blocks)
	m.blocks = append(m.blocks, block{kind: kind, content: content, dirty: true})
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
