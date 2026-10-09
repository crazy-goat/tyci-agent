package display

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/crazy-goat/tyci-agent/session"
)

// tuiMsgCompaction asks the TUI for a compaction divider. It is sent by
// TUI.Compaction, and by the agent view for an in-loop compaction of a
// subagent.
type tuiMsgCompaction struct {
	kind         string
	summarized   bool
	tokensBefore int // 0 = unknown
	time         time.Time
}

// newCompactionMsg converts the compaction meta of a session event or a
// subagent's live transcript. A zero time means "now".
func newCompactionMsg(meta session.CompactMeta) tuiMsgCompaction {
	at := meta.At
	if at.IsZero() {
		at = time.Now()
	}
	return tuiMsgCompaction{
		kind:         meta.Kind,
		summarized:   meta.Summarized,
		tokensBefore: meta.TokensBefore,
		time:         at,
	}
}

// Compaction shows a divider where the model's context was replaced. The text
// before it stays in the transcript. It is also used by the session replay,
// with the time of the stored event in meta.At.
func (t *TUI) Compaction(meta session.CompactMeta) {
	t.flushNow()
	t.prog.Send(newCompactionMsg(meta))
}

// handleCompactionMsg appends a divider block. It follows the same steps as
// the "block" case of handleBlockMsg.
func (m *TuiModel) handleCompactionMsg(msg tuiMsgCompaction) {
	m.forceRenderDirtyBlocks()
	idx := len(m.blocks)
	m.blocks = append(m.blocks, block{kind: "compaction", content: compactionDividerText(msg), dirty: true})
	m.dirtyBlocks[idx] = true
	m.invalidateTotalLines()
	m.maybeFlushOldBlocks()
}

// compactionDividerText is the text inside the divider, for example
// "auto compaction (summarized) · 412k tok · 14:32". The sizes are left out
// when unknown. A stored event without a kind (older session files) shows
// "compaction".
func compactionDividerText(msg tuiMsgCompaction) string {
	label := sanitizeUntrusted(msg.kind)
	if label == "" {
		label = "compaction"
	}
	if msg.summarized {
		label += " (summarized)"
	}
	parts := []string{label}
	if msg.tokensBefore > 0 {
		parts = append(parts, fmtTokens(msg.tokensBefore)+" tok")
	}
	if !msg.time.IsZero() {
		parts = append(parts, msg.time.Local().Format("15:04"))
	}
	return strings.Join(parts, " · ")
}

// renderCompactionDivider draws text centred between rules, across exactly
// width columns. A text too long for the width is cut with an ellipsis, so the
// line never wraps.
func renderCompactionDivider(text string, width int) string {
	const rule = "─"
	if width < 4 {
		return strings.Repeat(rule, max(width, 0))
	}
	inner := truncateStatusText(" "+text+" ", width-2)
	innerW := lipgloss.Width(inner)
	left := (width - innerW) / 2
	right := width - innerW - left
	line := strings.Repeat(rule, left) + inner + strings.Repeat(rule, right)
	return lipgloss.NewStyle().Foreground(lipgloss.Color("247")).Render(line)
}
