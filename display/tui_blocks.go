package display

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/decodo/tyci-agent/stream"
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
		m.invalidateAllBlockLineCounts()

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
		// For subagent: extract task description, don't append raw delta to inline block
		if m.subagentModalToolIdx >= 0 && m.subagentModalTitle == "subagent" && msg.content != "" {
			var args map[string]any
			if json.Unmarshal([]byte(msg.content), &args) == nil {
				if task, ok := args["task"].(string); ok && task != "" {
					m.subagentModalTitle = truncateString(task, 80)
				}
			}
			// Set inline block to "subagent (task...)" format
			if len(m.toolQueue) > m.subagentModalToolIdx {
				bidx := m.toolQueue[m.subagentModalToolIdx]
				if bidx >= 0 && bidx < len(m.blocks) && m.blocks[bidx].kind == "tool" {
					m.blocks[bidx].content = "subagent (" + m.subagentModalTitle + ")"
					m.blocks[bidx].cachedLines = nil
					delete(m.toolDisplayCache, bidx)
				}
			}
		} else {
			m.appendToLastTool(msg.content)
		}
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
		} else {
			m.appendTool(msg.toolIdx, msg.content)
		}
	case "usage":
		m.lastUsage = msg.usage
		m.lastStats = msg.stats
	case "error":
		// New error block → force-render previous dirty blocks
		m.forceRenderDirtyBlocks()
		idx := len(m.blocks)
		m.blocks = append(m.blocks, block{kind: "error", content: msg.content, dirty: true})
		m.dirtyBlocks[idx] = true
		m.invalidateAllBlockLineCounts()
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
		m.invalidateAllBlockLineCounts()
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
		m.streamingCache = make(map[int]string)
		m.toolDisplayCache = make(map[int]string)
		m.cachedTotalLines = -1
		m.subagentModalActive = false
		m.subagentModalContent.Reset()
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
			return
		}
		last.content += content
		last.dirty = true
		last.rendered = "" // invalidate cache
		last.cachedLineCount = 0
		last.cachedLines = nil
		idx := len(m.blocks) - 1
		m.dirtyBlocks[idx] = true
		delete(m.streamingCache, idx)
		delete(m.mdCacheRendered, idx)
		return
	}
	// New block type starting → force-render all dirty blocks immediately
	// so they show final markdown before the new block appears.
	m.forceRenderDirtyBlocks()
	idx := len(m.blocks)
	m.blocks = append(m.blocks, block{kind: kind, content: content, dirty: true})
	m.dirtyBlocks[idx] = true
	// Adding a block changes separator positions and line offsets. Recompute
	// layout caches right away instead of relying on a resize to fix them.
	m.invalidateAllBlockLineCounts()
}

// ─── Glamour renderer cache ─────────────────────────────────────────
