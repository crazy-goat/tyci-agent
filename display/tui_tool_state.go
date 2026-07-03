package display

import (
	"strings"
	"time"
)

func (m *TuiModel) appendToLastTool(delta string) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == "tool" {
			m.blocks[i].content += delta
			// The collapsed tool line only changes once the args JSON is
			// complete (formatToolCall shows "tool(...)" until it parses).
			// Skip cache invalidation for deltas that cannot end the JSON —
			// otherwise every delta re-parses the whole accumulated args.
			if jsonMaybeComplete(m.blocks[i].content) {
				m.blocks[i].cachedLines = nil
				m.blocks[i].cachedLineCount = 0
				delete(m.toolDisplayCache, i)
				m.invalidateTotalLines()
			}
			return
		}
	}
}

func (m *TuiModel) appendTool(queueIdx int, content string) {
	if queueIdx < 0 || queueIdx >= len(m.toolQueue) {
		return
	}
	blockIdx := m.toolQueue[queueIdx]
	if blockIdx >= 0 && blockIdx < len(m.blocks) && m.blocks[blockIdx].kind == "tool" {
		m.blocks[blockIdx].output += content
		// Bound the raw tool output buffer so a chatty tool (e.g. bash cat-ing
		// a huge log) can't grow the heap unbounded. Keep the tail — that's
		// what the click-to-expand modal shows.
		if len(m.blocks[blockIdx].output) > tuiMaxToolOutput {
			m.blocks[blockIdx].output = capToolOutput(m.blocks[blockIdx].output, tuiMaxToolOutput)
		}
	}
}

func (m *TuiModel) finishToolAt(result string) {
	if len(m.toolQueue) == 0 {
		return
	}
	idx := m.toolQueue[0]
	m.toolQueue = m.toolQueue[1:]
	if idx >= 0 && idx < len(m.blocks) && m.blocks[idx].kind == "tool" {
		if result != "" {
			if m.blocks[idx].output != "" && !strings.HasSuffix(m.blocks[idx].output, "\n") {
				m.blocks[idx].output += "\n"
			}
			m.blocks[idx].output += result
		}
		// Apply the output cap after appending the final result too.
		if len(m.blocks[idx].output) > tuiMaxToolOutput {
			m.blocks[idx].output = capToolOutput(m.blocks[idx].output, tuiMaxToolOutput)
		}
		m.blocks[idx].toolState = "done"
		m.blocks[idx].duration = time.Since(m.blocks[idx].startTime)
		m.blocks[idx].cachedLines = nil
		m.blocks[idx].cachedLineCount = 0
		delete(m.toolDisplayCache, idx)
		m.invalidateTotalLines()
	}
}
