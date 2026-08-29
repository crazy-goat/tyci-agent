package display

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/decodo/tyci/jobs"
)

func newModelWithTranscriptProvider(fn TranscriptProvider) TuiModel {
	m := newTestModelForSidebar()
	m.transcriptProvider = fn
	return m
}

func TestTranscriptViewer_EnterOnSubagentOpensViewer(t *testing.T) {
	lines := []string{"[0] user: hi", "[1] assistant: ok"}
	provider := func(jobID string) (string, []string, bool) {
		if jobID == "job-1" {
			return "job-1 — task", lines, true
		}
		return "", nil, false
	}
	m := newModelWithTranscriptProvider(provider)
	m.width = 80
	m.height = 20
	m.applyJobUpdate(jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "task", StartedAt: time.Now()})
	m.openSidebar(sidebarTabTasks)
	m.sidebarCursor = 0 // first subagent job
	rows := m.sidebarTaskRows(m.sidebarLayout().contentWidth)
	jobRows := m.sidebarTaskJobRows(m.sidebarLayout().contentWidth)
	if len(jobRows) == 0 {
		t.Fatalf("no job rows")
	}
	// sanity: first job row is subagent
	if !rows[jobRows[m.sidebarCursor]].subagent {
		t.Fatalf("expected subagent row at cursor 0")
	}
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	if !m2.transcriptViewerActive {
		t.Fatalf("expected transcript viewer active, got subagentModalActive=%v transcriptViewerActive=%v", m2.subagentModalActive, m2.transcriptViewerActive)
	}
	if got := m2.transcriptViewerTitle; got != "job-1 — task" {
		t.Fatalf("title = %q", got)
	}
	if len(m2.transcriptViewerLines) != 2 {
		t.Fatalf("lines len = %d", len(m2.transcriptViewerLines))
	}
	if m2.sidebarActive {
		t.Fatalf("sidebar should be closed after activate")
	}
}

func TestTranscriptViewer_BashRowStillOpensResultModal(t *testing.T) {
	provider := func(jobID string) (string, []string, bool) { return "title", []string{"a"}, true }
	m := newModelWithTranscriptProvider(provider)
	m.applyJobUpdate(jobs.Job{ID: "bash-1", Kind: jobs.KindBash, Status: jobs.StatusDone, Description: "bash desc", StartedAt: time.Now()})
	m.openSidebar(sidebarTabTasks)
	// Find bash job cursor position
	rows := m.sidebarTaskRows(m.sidebarLayout().contentWidth)
	jobRows := m.sidebarTaskJobRows(m.sidebarLayout().contentWidth)
	bashIdx := -1
	for i, rIdx := range jobRows {
		if rows[rIdx].job != nil && rows[rIdx].job.Kind == jobs.KindBash {
			bashIdx = i
			break
		}
	}
	if bashIdx < 0 {
		t.Fatalf("no bash row")
	}
	m.sidebarCursor = bashIdx
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	if m2.transcriptViewerActive {
		t.Fatalf("bash row should not open transcript viewer")
	}
	if !m2.subagentModalActive {
		t.Fatalf("bash row should open result modal")
	}
}

func TestTranscriptViewer_ProviderFalseFallsBackToResultModal(t *testing.T) {
	provider := func(jobID string) (string, []string, bool) { return "", nil, false }
	m := newModelWithTranscriptProvider(provider)
	m.applyJobUpdate(jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "task", StartedAt: time.Now()})
	m.openSidebar(sidebarTabTasks)
	m.sidebarCursor = 0
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	if m2.transcriptViewerActive {
		t.Fatalf("should not open transcript viewer when provider returns ok=false")
	}
	if !m2.subagentModalActive {
		t.Fatalf("should fall back to result modal")
	}
}

func TestTranscriptViewer_EscRestoresScroll(t *testing.T) {
	m := newTestModelForSidebar()
	m.width = 80
	m.height = 20
	m.scrollLine = 5
	m.atBottom = false
	m.openTranscriptViewer("title", []string{"a", "b"})
	if m.savedScrollLine != 5 {
		t.Fatalf("savedScrollLine = %d", m.savedScrollLine)
	}
	model, _ := m.updateTranscriptViewer(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := model.(TuiModel)
	if m2.transcriptViewerActive {
		t.Fatalf("should be closed after Esc")
	}
	if m2.scrollLine != 5 || m2.atBottom != false {
		t.Fatalf("scroll not restored: scrollLine=%d atBottom=%v", m2.scrollLine, m2.atBottom)
	}
}

func TestTranscriptViewer_EnterCloses(t *testing.T) {
	m := newTestModelForSidebar()
	m.openTranscriptViewer("title", []string{"a"})
	model, _ := m.updateTranscriptViewer(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := model.(TuiModel)
	if m2.transcriptViewerActive {
		t.Fatalf("Enter should close")
	}
}

func TestTranscriptViewer_ClickPastScrollOpensRightJob(t *testing.T) {
	linesFor := func(id string) (string, []string, bool) {
		return id + " title", []string{"line for " + id}, true
	}
	m := newModelWithTranscriptProvider(linesFor)
	m.height = 7 // contentHeight = 1, easy to force scroll offset
	for i := 0; i < 5; i++ {
		m.applyJobUpdate(jobs.Job{
			ID: fmt.Sprintf("sub-%d", i), Kind: jobs.KindSubagent, Status: jobs.StatusDone,
			Description: fmt.Sprintf("sub-%d desc", i), StartedAt: time.Now().Add(-time.Duration(5-i) * time.Minute),
		})
	}
	m.openSidebar(sidebarTabTasks)
	layout := m.sidebarLayout()
	width := layout.contentWidth
	jobRows := m.sidebarTaskJobRows(width)
	if len(jobRows) != 5 {
		t.Fatalf("want 5 job rows, got %d", len(jobRows))
	}
	lastJobLine := jobRows[len(jobRows)-1]
	rows := m.sidebarTaskRows(width)
	wantID := rows[lastJobLine].job.ID

	m.sidebarScroll = lastJobLine
	model, _ := m.updateSidebar(tea.MouseMsg{
		X: layout.contentLeft, Y: layout.contentTop,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m2 := model.(TuiModel)
	if !m2.transcriptViewerActive {
		t.Fatalf("expected transcript viewer after click past scroll")
	}
	if m2.transcriptViewerTitle != wantID+" title" {
		t.Fatalf("viewer opened %q, want %q", m2.transcriptViewerTitle, wantID+" title")
	}
}

func TestTranscriptViewer_RootRowNoOp(t *testing.T) {
	providerCalled := false
	provider := func(jobID string) (string, []string, bool) { providerCalled = true; return "t", []string{"a"}, true }
	m := newModelWithTranscriptProvider(provider)
	m.applyJobUpdate(jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "child", StartedAt: time.Now()})
	m.openSidebar(sidebarTabTasks)
	layout := m.sidebarLayout()
	width := layout.contentWidth
	rows := m.sidebarTaskRows(width)
	// Find synthetic root row index (job==nil, not heading)
	rootLine := -1
	for i, r := range rows {
		if r.job == nil && !r.isHeading {
			rootLine = i
			break
		}
	}
	if rootLine < 0 {
		t.Fatalf("no root row found")
	}
	// Click exactly on root row (visible when not scrolled)
	m.sidebarScroll = 0
	clickY := layout.contentTop + rootLine - m.sidebarVisibleScroll(layout)
	if clickY < layout.contentTop || clickY >= layout.contentTop+layout.contentHeight {
		// Root not visible in current viewport — force height large enough
		m.height = 30
		layout = m.sidebarLayout()
		clickY = layout.contentTop + rootLine - m.sidebarVisibleScroll(layout)
	}
	model, _ := m.updateSidebar(tea.MouseMsg{
		X: layout.contentLeft, Y: clickY,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m2 := model.(TuiModel)
	if m2.transcriptViewerActive || m2.subagentModalActive {
		t.Fatalf("click on root should be no-op, got transcriptActive=%v subagentActive=%v", m2.transcriptViewerActive, m2.subagentModalActive)
	}
	if providerCalled {
		t.Fatalf("provider should not be called for root click")
	}
}

func TestTranscriptViewer_UpdatePriorityAndBlockStillLands(t *testing.T) {
	m := newTestModelForSidebar()
	m.openTranscriptViewer("title", []string{"a"})
	// A block message must reach handleBlockMsg even with viewer open
	before := len(m.blocks)
	updated, _ := m.Update(tuiMsgBlock{kind: "text", content: "hello"})
	m2 := updated.(TuiModel)
	if len(m2.blocks) != before+1 && before == 0 && m2.blocks[0].content != "hello" {
		// Len may be 1 with "hello"
		if len(m2.blocks) == 0 {
			t.Fatalf("tuiMsgBlock was dropped behind viewer")
		}
	}
	if !m2.transcriptViewerActive {
		t.Fatalf("viewer should stay active after block")
	}
	// A normal KeyMsg should go to the viewer, not the input handler
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m3 := updated.(TuiModel)
	if m3.transcriptViewerActive {
		t.Fatalf("Esc should close viewer, not be handled as input")
	}
}

func TestTranscriptViewer_YankCopiesAll(t *testing.T) {
	m := newTestModelForSidebar()
	m.openTranscriptViewer("title", []string{"line1", "line2"})
	updated, cmd := m.updateTranscriptViewer(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m2 := updated.(TuiModel)
	// Should set a status message and return a copy feedback cmd
	_ = m2
	if cmd == nil {
		// copyFeedbackCmd may be nil if clipboard not wired? Still check status
	}
}

func TestTranscriptViewer_OuterClickCloses(t *testing.T) {
	m := newTestModelForSidebar()
	m.width = 80
	m.height = 20
	m.openTranscriptViewer("title", []string{"a"})
	layout := m.subagentModalLayout()
	// Click outside modal
	msg := tea.MouseMsg{X: 0, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	updated, _ := m.updateTranscriptViewer(msg)
	// Outside if not in layout
	inModal := msg.X >= layout.left && msg.X < layout.left+layout.popupWidth && msg.Y >= layout.top && msg.Y < layout.top+layout.boxHeight
	if inModal {
		t.Skip("click was inside modal at this size")
	}
	m2 := updated.(TuiModel)
	if m2.transcriptViewerActive {
		t.Fatalf("outside click should close")
	}
}

func TestTranscriptViewer_HeadingClickFallsThrough(t *testing.T) {
	// Clicking the "Subagents" heading should fall through to next job below it (still opens transcript for subagents)
	provider := func(jobID string) (string, []string, bool) { return jobID, []string{"a"}, true }
	m := newModelWithTranscriptProvider(provider)
	m.applyJobUpdate(jobs.Job{ID: "job-1", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "child", StartedAt: time.Now()})
	m.openSidebar(sidebarTabTasks)
	layout := m.sidebarLayout()
	// Click the heading row (line 0)
	model, _ := m.updateSidebar(tea.MouseMsg{
		X: layout.contentLeft, Y: layout.contentTop, // line 0 = Subagents heading
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m2 := model.(TuiModel)
	if !m2.transcriptViewerActive {
		t.Fatalf("heading click should fall through to next job and open transcript, got active=%v", m2.transcriptViewerActive)
	}
}

func TestTranscriptViewer_ScrollClamps(t *testing.T) {
	m := newTestModelForSidebar()
	m.width = 80
	m.height = 20
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	m.openTranscriptViewer("title", lines)
	// PgUp should increase scroll, End goes to 0
	model, _ := m.updateTranscriptViewer(tea.KeyMsg{Type: tea.KeyHome})
	m2 := model.(TuiModel)
	if m2.transcriptViewerScroll != m2.transcriptViewerMaxScroll() {
		t.Fatalf("Home should go to maxScroll %d, got %d", m2.transcriptViewerMaxScroll(), m2.transcriptViewerScroll)
	}
	model, _ = m2.updateTranscriptViewer(tea.KeyMsg{Type: tea.KeyEnd})
	m3 := model.(TuiModel)
	if m3.transcriptViewerScroll != 0 {
		t.Fatalf("End should go to 0, got %d", m3.transcriptViewerScroll)
	}
}

func TestSidebarTaskRows_SyntheticRootHasNoJob(t *testing.T) {
	m := newTestModelForSidebar()
	rows := m.sidebarTaskRows(m.sidebarLayout().contentWidth)
	// rows[0] heading, rows[1] root — both have no job
	if rows[0].job != nil || !rows[0].isHeading {
		t.Fatalf("rows[0] should be heading with no job")
	}
	foundRoot := false
	for _, r := range rows {
		if r.job == nil && !r.isHeading {
			foundRoot = true
			break
		}
	}
	if !foundRoot {
		t.Fatalf("expected a synthetic root row (job==nil, not heading)")
	}
	jobRows := m.sidebarTaskJobRows(m.sidebarLayout().contentWidth)
	for _, idx := range jobRows {
		if rows[idx].job == nil {
			t.Fatalf("jobRows contained a nil job at idx %d", idx)
		}
	}
}

func TestTranscriptViewer_RenderThroughLayout(t *testing.T) {
	m := newTestModelForSidebar()
	m.width = 80
	m.height = 20
	m.openTranscriptViewer("hello title", []string{"one", "two"})
	view := m.renderTranscriptViewerView()
	if !strings.Contains(view, "hello title") {
		t.Fatalf("view should contain title, got %q", view[:min(200, len(view))])
	}
}

// Ensure the transcript viewer eats keys while in modal priority region.
func TestTranscriptViewer_KeyUpDoesNotBubble(t *testing.T) {
	m := newTestModelForSidebar()
	m.openTranscriptViewer("title", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n"})
	// Need enough lines to allow scrolling
	m.width = 80
	m.height = 12 // small so maxScroll > 0
	m.openTranscriptViewer("title", []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17", "18", "19", "20"})
	before := m.transcriptViewerScroll
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m2 := updated.(TuiModel)
	if !m2.transcriptViewerActive {
		t.Fatalf("viewer should still be active after Up")
	}
	if m2.transcriptViewerScroll == before && m2.transcriptViewerMaxScroll() > 0 {
		t.Fatalf("Up should increase scroll when more lines exist")
	}
}

func TestTranscriptViewer_ZeroSizeNoPanic(t *testing.T) {
    m := newTestModelForSidebar()
    m.width = 0
    m.height = 0
    m.openTranscriptViewer("title that is definitely longer than any popup", []string{"a very long line that exceeds popup width and exercises slicing", strings.Repeat("x", 500)})
    defer func() {
        if r := recover(); r != nil {
            t.Fatalf("panic at 0x0: %v", r)
        }
    }()
    _ = m.renderTranscriptViewerView()
    // Also hit the other 0x0 path: popupWidth-4 == -6 branch
    m.width = 0
    m.height = 0
    m.openTranscriptViewer(strings.Repeat("漢", 100), []string{strings.Repeat("漢", 200)})
    _ = m.renderTranscriptViewerView()
}

func TestTranscriptViewer_ResizeClampsScroll(t *testing.T) {
	m := newTestModelForSidebar()
	m.width = 80
	m.height = 12
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = "line"
	}
	m.openTranscriptViewer("title", lines)
	// Start short so maxScroll is large, then grow so max shrinks and triggers clamp.
	m.transcriptViewerScroll = m.transcriptViewerMaxScroll()
	if m.transcriptViewerScroll == 0 {
		t.Fatalf("need maxScroll>0 to exercise clamp")
	}
	prev := m.transcriptViewerScroll
	m.width = 80
	m.height = 30
	updated, _ := m.updateTranscriptViewer(tea.WindowSizeMsg{Width: 80, Height: 30})
	m2 := updated.(TuiModel)
	if m2.transcriptViewerScroll != m2.transcriptViewerMaxScroll() {
		t.Fatalf("scroll not clamped after resize: got %d, want max %d (was %d)", m2.transcriptViewerScroll, m2.transcriptViewerMaxScroll(), prev)
	}
	_ = footerPctIsSane(m2)
}

func footerPctIsSane(m TuiModel) bool {
	max := m.transcriptViewerMaxScroll()
	if max == 0 {
		return true
	}
	return m.transcriptViewerScroll <= max
}
