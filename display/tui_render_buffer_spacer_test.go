package display

import "testing"

// The two flat-line builders compute offsets independently; if they disagree
// about one spacer, every later line index shifts and mouse selection starts
// landing on the wrong row. Pin the rule itself, then pin that both agree.
func TestSpacerAfter_ToolsAndThinkingPackTogether(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"tool", "tool", false},
		{"thinking", "thinking", false},
		{"tool", "thinking", false},
		{"thinking", "tool", false},
		{"thinking", "text", true},
		{"text", "thinking", true},
		{"tool", "text", true},
		{"text", "text", true},
		{"thinking", "error", true},
		{"user", "thinking", true},
	}
	for _, c := range cases {
		m := &TuiModel{blocks: []block{{kind: c.a}, {kind: c.b}}}
		if got := m.spacerAfter(0); got != c.want {
			t.Errorf("spacerAfter(%s → %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestSpacerAfter_LastBlockNeverHasOne(t *testing.T) {
	m := &TuiModel{blocks: []block{{kind: "text"}}}
	if m.spacerAfter(0) {
		t.Fatal("the last block must not be followed by a spacer")
	}
}

// A run of thinking and tools should produce no blank lines inside it — only
// the closing prose gets separated. Built through the real message path so the
// render caches exist, the way the other render tests do.
func TestBuildAllFlatRenderLines_NoSpacersInAThinkingToolRun(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width, m.height = 80, 24
	m.handleBlockMsg(tuiMsgBlock{kind: "thinking", content: "weighing options"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", toolName: "read", content: "ok"})
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "done"})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.forceRenderDirtyBlocks()

	var spacers int
	for _, l := range m.buildAllFlatRenderLines() {
		if l.SourceKind == "spacer" {
			spacers++
		}
	}
	if spacers != 1 {
		t.Fatalf("got %d spacers, want exactly 1 (only before the closing text)", spacers)
	}
}
