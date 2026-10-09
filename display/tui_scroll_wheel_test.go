package display

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crazy-goat/tyci-agent/internal/pricing"
)

// useFixtureCatalog points HOME at a temporary directory with a providers.json
// of 40 providers and 50 models each. The status bar looks up the model context
// limit on every redraw, so the catalog size sets the cost of an unknown model
// id. Without the fixture, the cost would depend on the developer's own catalog.
// The model "test/model" that newTestModel uses is not in the fixture, so it
// takes the full-scan path.
func useFixtureCatalog(tb testing.TB) {
	tb.Helper()
	home := tb.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".tyci"), 0o755); err != nil {
		tb.Fatal(err)
	}
	catalog := map[string]any{}
	for p := 0; p < 40; p++ {
		models := map[string]any{}
		for m := 0; m < 50; m++ {
			id := fmt.Sprintf("model-%03d", m)
			models[id] = map[string]any{
				"id":    id,
				"name":  "Model " + id,
				"cost":  map[string]any{"input": 1, "output": 2},
				"limit": map[string]any{"context": 100000, "output": 8000},
			}
		}
		pid := fmt.Sprintf("provider-%02d", p)
		catalog[pid] = map[string]any{"id": pid, "name": pid, "models": models}
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".tyci", "providers.json"), data, 0o644); err != nil {
		tb.Fatal(err)
	}
	tb.Setenv("HOME", home)
	pricing.Reset()
	tb.Cleanup(pricing.Reset)
}

// newWheelBenchModel returns a 120 x 40 model with `blocks` text blocks, using
// the fixture catalog. Each block renders about 1 KiB, so the resident budget is
// exceeded and the oldest blocks are flushed to the scrollback file, as in a
// long session. The scrollback file is removed when tb ends. The benchmarks
// also close it at the end of each iteration, because b.N iterations would
// otherwise keep one file each until the benchmark ends.
func newWheelBenchModel(tb testing.TB, blocks int) TuiModel {
	tb.Helper()
	useFixtureCatalog(tb)
	m := newTestModel()
	tb.Cleanup(m.scrollback.close)
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

// The setup of each benchmark runs once per iteration, outside the timer, so run
// them with a fixed count, for example -benchtime 100x, to keep the run short.
//
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
		b.StopTimer()
		m.scrollback.close()
	}
}

// BenchmarkScrollWheelStreaming is BenchmarkScrollWheel with a streamed text
// delta before each wheel event, as while an agent streams. The delta goes
// through handleBlockMsg, the same path as a model token.
func BenchmarkScrollWheelStreaming(b *testing.B) {
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		b.StopTimer()
		m := newWheelBenchModel(b, 500)
		b.StartTimer()
		for i := 0; i < 100; i++ {
			m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "streamed token "})
			m = wheelUp(m)
		}
		_ = m.View()
		b.StopTimer()
		m.scrollback.close()
	}
}

// BenchmarkScrollWheelInvalidated is BenchmarkScrollWheel with
// invalidateTotalLines before each wheel event. That sets cachedTotalLines to -1,
// the suspect in issue #595, so each event recounts every block.
func BenchmarkScrollWheelInvalidated(b *testing.B) {
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		b.StopTimer()
		m := newWheelBenchModel(b, 500)
		b.StartTimer()
		for i := 0; i < 100; i++ {
			m.invalidateTotalLines()
			m = wheelUp(m)
		}
		_ = m.View()
		b.StopTimer()
		m.scrollback.close()
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
