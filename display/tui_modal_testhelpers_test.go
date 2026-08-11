package display

// seedModalBlock appends a finished tool block holding out and points the modal
// at it, mirroring what a click on that block does. The modal has no content of
// its own, so tests must go through a block to give it something to render.
func seedModalBlock(m *TuiModel, toolName, out string) int {
	idx := len(m.blocks)
	m.blocks = append(m.blocks, block{
		kind: "tool", toolName: toolName, toolState: "done",
		collapsed: true, maxLines: defaultMaxLines(toolName), output: out,
	})
	m.subagentModalBlockIdx = idx
	m.invalidateTotalLines()
	return idx
}
