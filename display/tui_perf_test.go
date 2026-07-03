package display

import (
	"strings"
	"testing"
	"time"
)

// ─── streamWrap incremental wrapping ─────────────────────────────────────

// feedStreamWrap streams content into a streamWrap in the given chunks and
// returns the final render output.
func feedStreamWrap(t *testing.T, chunks []string, useBar bool, width int) (string, []string) {
	t.Helper()
	sw := &streamWrap{}
	var content strings.Builder
	var out string
	var lines []string
	for _, c := range chunks {
		content.WriteString(c)
		out, lines = sw.render(content.String(), useBar, width)
	}
	return out, lines
}

func TestStreamWrapMatchesFullWrap(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
	}{
		{"single chunk", []string{"hello world"}},
		{"word by word", []string{"hello ", "world ", "this ", "is ", "a ", "test"}},
		{"newline in middle", []string{"line one\nline tw", "o continues"}},
		{"chunk ends with newline", []string{"line one\n", "line two\n", "line three"}},
		{"multiple newlines in one chunk", []string{"a\nb\nc\nd", "\ne\nf"}},
		{"trailing newlines", []string{"text\n\n\n"}},
		{"empty lines between", []string{"a\n\nb", "\n\nc"}},
		{"long line that soft-wraps", []string{strings.Repeat("word ", 20), strings.Repeat("more ", 20)}},
		{"long line built incrementally", []string{strings.Repeat("x", 30), strings.Repeat("y", 30), strings.Repeat("z", 30)}},
		{"unicode", []string{"zażółć ", "gęślą\njaźń ", "日本語のテキスト"}},
		{"single char chunks", strings.Split("abc def\nghi jkl mno pqr stu vwx\nyz", "")},
		{"leading newline", []string{"\nafter empty first line"}},
		{"only newlines", []string{"\n", "\n"}},
	}
	widths := []int{20, 40, 80}
	for _, tc := range cases {
		for _, width := range widths {
			for _, useBar := range []bool{false, true} {
				full := strings.Join(tc.chunks, "")
				want := wrapRawText(full, useBar, width)
				got, lines := feedStreamWrap(t, tc.chunks, useBar, width)
				if got != want {
					t.Errorf("%s (width=%d bar=%v): incremental output diverged\n got: %q\nwant: %q",
						tc.name, width, useBar, got, want)
				}
				wantCount := lineCount(want)
				if len(lines) != wantCount {
					t.Errorf("%s (width=%d bar=%v): line count %d, want %d",
						tc.name, width, useBar, len(lines), wantCount)
				}
				if want != "" && strings.Join(lines, "\n") != want {
					t.Errorf("%s (width=%d bar=%v): lines don't match output", tc.name, width, useBar)
				}
			}
		}
	}
}

func TestStreamWrapRepeatedRenderIsStable(t *testing.T) {
	sw := &streamWrap{}
	content := "some text\nmore text"
	first, _ := sw.render(content, false, 40)
	second, _ := sw.render(content, false, 40)
	if first != second {
		t.Errorf("repeated render changed output: %q vs %q", first, second)
	}
	if second != wrapRawText(content, false, 40) {
		t.Errorf("cached render diverged from wrapRawText")
	}
}

func TestStreamWrapRecoversFromContentShrink(t *testing.T) {
	sw := &streamWrap{}
	sw.render("a long piece of content\nwith lines", false, 40)
	// Content replaced with something shorter — must restart, not panic.
	got, _ := sw.render("short", false, 40)
	if want := wrapRawText("short", false, 40); got != want {
		t.Errorf("after shrink got %q, want %q", got, want)
	}
}

// ─── append paths must not invalidate earlier blocks' render caches ──────

// newTestModelWithRenderedBlock returns a model with one finished text block
// whose glamour render is cached.
func newTestModelWithRenderedBlock(t *testing.T) TuiModel {
	t.Helper()
	m := newModel(make(chan string, 1), "test-model", "", nil, nil, nil, nil)
	m.width = 80
	m.height = 24
	m.status = "idle"
	m.blocks = append(m.blocks, block{kind: "text", content: "# Hello\n\nSome *markdown* text.", dirty: true})
	m.dirtyBlocks[0] = true
	m.forceRenderDirtyBlocks()
	if _, ok := m.mdCacheRendered[0]; !ok {
		t.Fatal("setup: expected block 0 to have a cached markdown render")
	}
	if m.blocks[0].cachedLines == nil {
		t.Fatal("setup: expected block 0 to have cached lines")
	}
	return m
}

func TestToolStartKeepsEarlierRenderCaches(t *testing.T) {
	m := newTestModelWithRenderedBlock(t)
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read"})
	if _, ok := m.mdCacheRendered[0]; !ok {
		t.Error("tool-start wiped the markdown cache of an earlier block (forces glamour re-render of full history)")
	}
	if m.blocks[0].cachedLines == nil {
		t.Error("tool-start wiped cached lines of an earlier block")
	}
}

func TestErrorAndBlockMsgsKeepEarlierRenderCaches(t *testing.T) {
	for _, kind := range []string{"error", "block"} {
		m := newTestModelWithRenderedBlock(t)
		m.handleBlockMsg(tuiMsgBlock{kind: kind, content: "boom"})
		if _, ok := m.mdCacheRendered[0]; !ok {
			t.Errorf("%s message wiped the markdown cache of an earlier block", kind)
		}
	}
}

func TestResizeStillInvalidatesRenderCaches(t *testing.T) {
	m := newTestModelWithRenderedBlock(t)
	m.invalidateAllBlockLineCounts()
	if _, ok := m.mdCacheRendered[0]; ok {
		t.Error("resize invalidation must clear markdown caches (wrap width changed)")
	}
	if m.blocks[0].cachedLines != nil {
		t.Error("resize invalidation must clear cached lines")
	}
}

// ─── tool-delta cheap path ───────────────────────────────────────────────

func TestToolDeltaSkipsInvalidationForPartialJSON(t *testing.T) {
	m := newModel(make(chan string, 1), "test-model", "", nil, nil, nil, nil)
	m.width = 80
	m.height = 24
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "write"})
	// Prime the display cache as a render would.
	_ = m.renderToolBlock(0, m.blocks[0])
	if _, ok := m.toolDisplayCache[0]; !ok {
		t.Fatal("setup: expected tool display cache entry")
	}

	// Partial JSON deltas must not invalidate the display cache.
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `{"path":"a.go","content":"partial`})
	if _, ok := m.toolDisplayCache[0]; !ok {
		t.Error("partial tool-delta invalidated the display cache")
	}

	// Once the JSON completes, the cache must be invalidated so the final
	// summary (with parsed args) shows up.
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-delta", content: `..."}`})
	if _, ok := m.toolDisplayCache[0]; ok {
		t.Error("completing tool-delta did not invalidate the display cache")
	}
	if got := formatToolCall(m.blocks[0].toolName, m.blocks[0].content); got != `write(a.go)` {
		t.Errorf("final summary = %q, want %q", got, "write(a.go)")
	}
}

func TestJSONMaybeComplete(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`{"a":1}`, true},
		{`{"a":1}  ` + "\n", true},
		{`{"a":`, false},
		{`{"a":"text`, false},
		{``, false},
	}
	for _, tc := range cases {
		if got := jsonMaybeComplete(tc.in); got != tc.want {
			t.Errorf("jsonMaybeComplete(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// ─── lazy PlainText ──────────────────────────────────────────────────────

func TestRenderLinePlainLazy(t *testing.T) {
	styled := "\x1b[38;5;150mhello\x1b[0m world"
	l := RenderLine{Text: styled}
	if got := l.plain(); got != "hello world" {
		t.Errorf("plain() = %q, want %q", got, "hello world")
	}
	// Pre-filled PlainText (as tests and legacy callers construct) wins.
	l2 := RenderLine{Text: styled, PlainText: "prefilled"}
	if got := l2.plain(); got != "prefilled" {
		t.Errorf("plain() = %q, want %q", got, "prefilled")
	}
}

// ─── adaptive streaming coalescing ───────────────────────────────────────

func TestNextCoalesce(t *testing.T) {
	// First flush after a quiet period paints fast.
	if got := nextCoalesce(time.Hour); got != coalesceCold {
		t.Errorf("cold stream: got %v, want %v", got, coalesceCold)
	}
	// Sustained stream batches harder.
	if got := nextCoalesce(50 * time.Millisecond); got != coalesceHot {
		t.Errorf("hot stream: got %v, want %v", got, coalesceHot)
	}
}

// ─── jump-scroll: rows stay stationary while the agent streams ────────────

func TestJumpScrollStart(t *testing.T) {
	const msgHeight = 20 // jump = 5 (quarter of the window)
	prev := 0
	for minStart := 1; minStart < 100; minStart++ {
		got := jumpScrollStart(minStart, msgHeight)
		if got < minStart {
			t.Fatalf("minStart=%d: start %d must not cut off the newest lines", minStart, got)
		}
		if got%5 != 0 {
			t.Fatalf("minStart=%d: start %d not a multiple of the jump size", minStart, got)
		}
		if got < prev {
			t.Fatalf("minStart=%d: start went backwards (%d < %d)", minStart, got, prev)
		}
		prev = got
	}
	// Degenerate window: quantization disabled, exact bottom pin.
	if got := jumpScrollStart(7, 1); got != 7 {
		t.Errorf("msgHeight=1: got %d, want 7", got)
	}
}

func TestJumpScrollKeepsRowsStationaryWhileStreaming(t *testing.T) {
	m := newModel(make(chan string, 1), "test-model", "", nil, nil, nil, nil)
	m.width = 80
	m.height = 24 // visibleLines = 24 - 3 (input) - 2 = 19, jump = 9
	for i := 0; i < 40; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "streamed line\n"})
	}
	if m.status != "responding" {
		t.Fatalf("setup: status = %q, want responding", m.status)
	}

	msgHeight := m.visibleLines()
	jump := msgHeight / 4
	starts := map[int]bool{}
	prevStart := -1
	for i := 0; i < 20; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "streamed line\n"})
		lines := m.buildFlatRenderLines()
		if len(lines) == 0 {
			t.Fatal("no visible lines")
		}
		start := lines[0].SourceLine
		if start%jump != 0 {
			t.Fatalf("append %d: viewport start %d not a multiple of jump %d", i, start, jump)
		}
		if start < prevStart {
			t.Fatalf("append %d: viewport start went backwards (%d < %d)", i, start, prevStart)
		}
		last := lines[len(lines)-1]
		if last.SourceLine != m.blocks[0].cachedLineCount-1 {
			t.Fatalf("append %d: newest line not visible (last=%d, want %d)",
				i, last.SourceLine, m.blocks[0].cachedLineCount-1)
		}
		starts[start] = true
		prevStart = start
	}
	// 20 appended lines with a quarter-screen jump → a handful of distinct
	// viewport positions, not 20 (which is what smooth per-line scrolling gives).
	if len(starts) > 6 {
		t.Errorf("viewport shifted %d times over 20 appends; jump-scroll should batch shifts", len(starts))
	}

	// Once the agent is idle, the view snaps back to exact bottom pin.
	m.status = "idle"
	lines := m.buildFlatRenderLines()
	wantStart := m.totalRenderedLines() - msgHeight
	if got := lines[0].SourceLine; got != wantStart {
		t.Errorf("idle: viewport start %d, want exact bottom pin %d", got, wantStart)
	}
	if got := len(lines); got != msgHeight {
		t.Errorf("idle: %d visible lines, want full window %d", got, msgHeight)
	}
}

// ─── benchmarks: streaming wrap cost per chunk ───────────────────────────

func streamingChunks() []string {
	chunk := "some streamed tokens arriving from the model "
	chunks := make([]string, 400)
	for i := range chunks {
		if i%8 == 7 {
			chunks[i] = chunk + "\n"
		} else {
			chunks[i] = chunk
		}
	}
	return chunks
}

// Old behavior: re-wrap the whole accumulated block on every chunk.
func BenchmarkStreamingWrapFull(b *testing.B) {
	chunks := streamingChunks()
	for b.Loop() {
		var content strings.Builder
		for _, c := range chunks {
			content.WriteString(c)
			_ = wrapRawText(content.String(), false, 100)
		}
	}
}

// New behavior: incremental wrap, only the last logical line per chunk.
func BenchmarkStreamingWrapIncremental(b *testing.B) {
	chunks := streamingChunks()
	for b.Loop() {
		sw := &streamWrap{}
		var content strings.Builder
		for _, c := range chunks {
			content.WriteString(c)
			_, _ = sw.render(content.String(), false, 100)
		}
	}
}
