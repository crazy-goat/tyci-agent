package display

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/tools"
)

func TestCompactionDividerText(t *testing.T) {
	at := time.Date(2026, 10, 9, 14, 32, 0, 0, time.Local)
	tests := []struct {
		name string
		msg  tuiMsgCompaction
		want string
	}{
		{"auto with size before", tuiMsgCompaction{kind: "auto compaction", tokensBefore: 412000, time: at},
			"auto compaction · 412k tok · 14:32"},
		{"summarized label", tuiMsgCompaction{kind: "auto compaction", summarized: true, tokensBefore: 412000, time: at},
			"auto compaction (summarized) · 412k tok · 14:32"},
		{"unknown sizes are left out", tuiMsgCompaction{kind: "/compact", time: at},
			"/compact · 14:32"},
		{"zero time is left out", tuiMsgCompaction{kind: "in-loop compaction"},
			"in-loop compaction"},
		{"empty kind falls back", tuiMsgCompaction{},
			"compaction"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compactionDividerText(tt.msg); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewCompactionMsgKeepsMeta(t *testing.T) {
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	msg := newCompactionMsg(session.CompactMeta{Kind: session.CompactKindTool, Summarized: true, TokensBefore: 5, At: at})
	if msg.kind != session.CompactKindTool || !msg.summarized || msg.tokensBefore != 5 || !msg.time.Equal(at) {
		t.Fatalf("msg = %+v", msg)
	}
	if now := newCompactionMsg(session.CompactMeta{}).time; now.IsZero() {
		t.Fatal("zero At should become now")
	}
}

func TestRenderCompactionDividerFullWidth(t *testing.T) {
	text := "auto compaction (summarized) · 412k tok · 14:32"
	for width := 36; width <= 80; width++ {
		line := renderCompactionDivider(text, width)
		if strings.Contains(line, "\n") {
			t.Fatalf("width %d: divider wraps: %q", width, line)
		}
		if got := ansi.StringWidth(line); got != width {
			t.Fatalf("width %d: divider is %d columns", width, got)
		}
		if width >= 60 && !strings.Contains(ansi.Strip(line), text) {
			t.Fatalf("width %d: label missing: %q", width, ansi.Strip(line))
		}
	}
}

func TestRenderCompactionDividerNarrowWidth(t *testing.T) {
	if got := ansi.Strip(renderCompactionDivider("x", 3)); got != "───" {
		t.Fatalf("narrow divider = %q", got)
	}
}

func TestCompactionBlockRendersAtNewWidth(t *testing.T) {
	m := newTestModel()
	m.width = 80
	m.blocks = append(m.blocks, block{kind: "compaction", content: "auto compaction · 412k tok · 14:32", dirty: true})
	if w := ansi.StringWidth(m.renderBlock(0, m.blocks[0])); w != 80 {
		t.Fatalf("width 80: block is %d columns", w)
	}
	m.width = 36
	if w := ansi.StringWidth(m.renderBlock(0, m.blocks[0])); w != 36 {
		t.Fatalf("width 36: block is %d columns", w)
	}
}

func TestHandleCompactionMsgAppendsDivider(t *testing.T) {
	m := newTestModel()
	m.handleCompactionMsg(newCompactionMsg(session.CompactMeta{Kind: session.CompactKindAuto, TokensBefore: 1000}))
	if n := len(m.blocks); n != 1 {
		t.Fatalf("blocks = %d, want 1", n)
	}
	if b := m.blocks[0]; b.kind != "compaction" || !strings.Contains(b.content, "auto compaction") {
		t.Fatalf("block = %+v", b)
	}
}

func TestPullAgentViewShowsInLoopCompaction(t *testing.T) {
	const jobID = "compaction-divider-agent-view"
	m := newTestModel()
	m.agentView = &agentView{jobID: jobID, label: "worker", model: newAgentViewModel()}
	tools.RecordLiveEvent(jobID, tools.LiveEvent{Kind: "text", Content: "before"})
	tools.RecordLiveEvent(jobID, tools.LiveEvent{Kind: "compaction", Compaction: session.CompactMeta{
		Kind: session.CompactKindInLoop, Summarized: true, TokensBefore: 180000, At: time.Now(),
	}})
	m.pullAgentView()
	blocks := m.agentView.model.blocks
	if len(blocks) != 2 {
		t.Fatalf("agent view blocks = %d, want 2", len(blocks))
	}
	if b := blocks[1]; b.kind != "compaction" || !strings.Contains(b.content, "in-loop compaction (summarized)") {
		t.Fatalf("divider = %+v", b)
	}
}

// TestUpdateCompactionMsgShowsDivider checks the path of a live divider: the
// message sent by TUI.Compaction goes through update and adds a block.
func TestUpdateCompactionMsgShowsDivider(t *testing.T) {
	m := newTestModel()
	model, _ := m.Update(newCompactionMsg(session.CompactMeta{Kind: session.CompactKindCommand, TokensBefore: 90000, At: time.Now()}))
	next := model.(TuiModel)
	if n := len(next.blocks); n != 1 {
		t.Fatalf("blocks = %d, want 1 divider", n)
	}
	if b := next.blocks[0]; b.kind != "compaction" || !strings.Contains(b.content, "/compact · 90k tok") {
		t.Fatalf("divider block = %+v", b)
	}
}
