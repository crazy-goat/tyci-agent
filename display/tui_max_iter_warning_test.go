package display

import (
	"strings"
	"testing"
)

// The agent sends the max-iterations warning through ToolBlock. The TUI turns
// a ToolBlock into a "block" message, so the warning must stay visible there.
func TestTuiModel_MaxIterationsWarningIsShownAsBlock(t *testing.T) {
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	const warning = "⚠️ Agent executed 3 tool-call iterations – possible infinite loop. Stopping."

	m.handleBlockMsg(tuiMsgBlock{kind: "block", content: warning})

	for _, b := range m.blocks {
		if b.kind == "block" && strings.Contains(b.content, "tool-call iterations") {
			return
		}
	}
	t.Errorf("warning not found in TUI blocks: %+v", m.blocks)
}
