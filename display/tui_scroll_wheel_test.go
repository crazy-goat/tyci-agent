package display

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// newWheelBenchModel returns a 120 x 40 model with `blocks` text blocks. Each
// block renders about 1 KiB, so the resident budget is exceeded and the oldest
// blocks are flushed to the scrollback file, as in a long session.
func newWheelBenchModel(tb testing.TB, blocks int) TuiModel {
	tb.Helper()
	m := newTestModel()
	body := strings.Repeat("lorem ipsum dolor sit amet consectetur ", 25)
	for i := 0; i < blocks; i++ {
		kind := "text"
		if i%2 == 0 {
			m.appendOrAppend(kind, fmt.Sprintf("You: %d %s", i, body))
		} else {
			m.appendOrAppend(kind, fmt.Sprintf("agent %d %s", i, body))
		}
		m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	}
	m.View()
	return m
}

// wheelUp sends one wheel-up event through Update and returns the new model.
func wheelUp(m TuiModel) TuiModel {
	next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	return next.(TuiModel)
}

// wheelDown sends one wheel-down event through Update and returns the new model.
func wheelDown(m TuiModel) TuiModel {
	next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	return next.(TuiModel)
}

// BenchmarkScrollWheel measures 100 wheel-up events followed by one View on a
// 500-block session, with the oldest blocks flushed. Each iteration starts from
// a fresh model, so every run pays the cold-scroll cost.
func BenchmarkScrollWheel(b *testing.B) {
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		b.StopTimer()
		m := newWheelBenchModel(b, 500)
		b.StartTimer()
		for i := 0; i < 100; i++ {
			m = wheelUp(m)
		}
		_ = m.View()
	}
}

// TestScrollWheel_ManyEventsStayFast sends 200 wheel events to a 500-block
// model. The limit is loose on purpose: it catches a return of the per-event
// full-transcript cost, not small timing noise on the CI runner.
func TestScrollWheel_ManyEventsStayFast(t *testing.T) {
	m := newWheelBenchModel(t, 500)
	start := time.Now()
	for i := 0; i < 200; i++ {
		m = wheelUp(m)
		_ = m.View()
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("200 wheel events took %v, want at most 2s", elapsed)
	}
}

// TestScrollWheel_UpThenDownReturnsToBottom checks that wheel-up and wheel-down
// are inverses: the viewport ends on the newest line and follows the bottom.
func TestScrollWheel_UpThenDownReturnsToBottom(t *testing.T) {
	m := newWheelBenchModel(t, 500)
	for i := 0; i < 10; i++ {
		m = wheelUp(m)
	}
	if m.scrollLine == 0 || m.atBottom {
		t.Fatalf("after wheel up: scrollLine=%d atBottom=%v, want scrolled away from bottom", m.scrollLine, m.atBottom)
	}
	for i := 0; i < 10; i++ {
		m = wheelDown(m)
	}
	if m.scrollLine != 0 {
		t.Fatalf("scrollLine = %d, want 0", m.scrollLine)
	}
	if !m.atBottom {
		t.Fatal("atBottom = false, want true after scrolling back down")
	}
}

// TestScrollWheel_PageInKeepsPosition checks that paging a flushed block back
// in does not move the viewport. The model is scrolled to the top, where the
// visible blocks are flushed, then a visible block is flushed and paged back.
func TestScrollWheel_PageInKeepsPosition(t *testing.T) {
	m := newWheelBenchModel(t, 500)
	for m.scrollLine < m.totalRenderedLines()-m.messageRegionHeight() {
		m = wheelUp(m)
	}
	top := m.scrollLine
	before := m.View()

	// Flush a visible block, then drop its caches, as the eviction path does.
	idx := -1
	for i := range m.blocks {
		if !m.blocks[i].flushed && m.blocks[i].cachedLines != nil {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("setup: no resident block at the top of the transcript")
	}
	m.scrollback.flushBlock(&m.blocks[idx], m.renderWidth())
	m.dropResidentCaches(idx)
	if !m.blocks[idx].flushed {
		t.Fatal("setup: block was not flushed")
	}

	m.invalidateMessageRegion()
	after := m.View()
	if m.scrollLine != top {
		t.Fatalf("scrollLine = %d after page-in, want %d", m.scrollLine, top)
	}
	if after != before {
		t.Fatal("visible rows changed after a flushed block was paged back in")
	}
}
