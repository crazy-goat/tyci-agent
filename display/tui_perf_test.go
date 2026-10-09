package display

import (
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	m := newModel(make(chan string, 1), "test-model", "", nil, 0, 0, 0)
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
	m := newModel(make(chan string, 1), "test-model", "", nil, 0, 0, 0)
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

// flushCounter is a bubbletea model that counts the transcript flushes it
// receives (tuiMsgBlock) and the text bytes they carry. Each flush is one
// repaint of the transcript.
type flushCounter struct {
	flushes *atomic.Int32
	bytes   *atomic.Int64
}

func (f flushCounter) Init() tea.Cmd { return nil }

func (f flushCounter) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if b, ok := msg.(tuiMsgBlock); ok {
		f.flushes.Add(1)
		f.bytes.Add(int64(len(b.content)))
	}
	return f, nil
}

func (f flushCounter) View() string { return "" }

// streamFor2sThroughFlushLoop calls send every 10ms for 2s through the real
// flushLoop and a real bubbletea program. It returns the number of calls, the
// repaints (flushes) the program received and the bytes those flushes carried.
func streamFor2sThroughFlushLoop(t *testing.T, send func(tui *TUI)) (calls, repaints int, bytes int64) {
	t.Helper()
	var flushes atomic.Int32
	var flushedBytes atomic.Int64
	p := tea.NewProgram(flushCounter{flushes: &flushes, bytes: &flushedBytes},
		tea.WithInput(nil), tea.WithoutRenderer(), tea.WithOutput(io.Discard))
	runDone := make(chan struct{})
	go func() {
		_, _ = p.Run()
		close(runDone)
	}()

	tui := &TUI{
		prog:      p,
		done:      make(chan struct{}),
		flushWake: make(chan struct{}, 1),
		flushDone: make(chan struct{}),
	}
	go tui.flushLoop()

	for start := time.Now(); time.Since(start) < 2*time.Second; {
		send(tui)
		calls++
		time.Sleep(10 * time.Millisecond)
	}
	close(tui.done)
	<-tui.flushDone
	p.Quit()
	<-runDone
	return calls, int(flushes.Load()), flushedBytes.Load()
}

// TestStreamFlushRepaintsOncePerInterval streams a chunk every 10ms for 2s
// through the real flushLoop and a real bubbletea program. With the 1s
// streamFlushInterval the TUI repaints at the first chunk and then about once
// per second, not once per chunk. The 2s end flush must deliver every byte.
func TestStreamFlushRepaintsOncePerInterval(t *testing.T) {
	const chunk = "streamed text "
	chunks, got, bytes := streamFor2sThroughFlushLoop(t, func(tui *TUI) { tui.Text(chunk) })

	// Flushes at about 0s, 1s and 2s, plus the end flush: far below the chunk count.
	t.Logf("%d chunks over 2s -> %d repaints", chunks, got)
	if got < 2 || got > 4 {
		t.Errorf("repaints = %d for %d chunks over 2s, want 2 to 4", got, chunks)
	}
	if want := int64(chunks * len(chunk)); bytes != want {
		t.Errorf("flushed %d bytes, want %d", bytes, want)
	}
}

// TestToolEventsRepaintOncePerInterval checks that tool-delta and tool-progress
// events go through the same 1s throttle as text. Without it each event posted
// its own message, so one repaint per event.
func TestToolEventsRepaintOncePerInterval(t *testing.T) {
	t.Run("tool-delta", func(t *testing.T) {
		const delta = `{"path":"a.go"} `
		chunks, got, bytes := streamFor2sThroughFlushLoop(t, func(tui *TUI) { tui.ToolCallDelta(delta) })
		t.Logf("%d tool deltas over 2s -> %d repaints", chunks, got)
		if got < 2 || got > 4 {
			t.Errorf("repaints = %d for %d tool deltas over 2s, want 2 to 4", got, chunks)
		}
		if want := int64(chunks * len(delta)); bytes != want {
			t.Errorf("flushed %d bytes, want %d", bytes, want)
		}
	})
	t.Run("tool-progress", func(t *testing.T) {
		const line = "progress line"
		chunks, got, bytes := streamFor2sThroughFlushLoop(t, func(tui *TUI) { tui.StreamProgress(0, line) })
		t.Logf("%d progress lines over 2s -> %d repaints", chunks, got)
		if got < 2 || got > 4 {
			t.Errorf("repaints = %d for %d progress lines over 2s, want 2 to 4", got, chunks)
		}
		if want := int64(chunks * (len(line) + 1)); bytes != want {
			t.Errorf("flushed %d bytes, want %d", bytes, want)
		}
	})
}

// ─── viewport pins to exact bottom while the agent streams ────────────────

func TestStreamingPinsToExactBottom(t *testing.T) {
	m := newModel(make(chan string, 1), "test-model", "", nil, 0, 0, 0)
	m.width = 80
	m.height = 24
	for i := 0; i < 40; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "streamed line\n"})
	}
	if m.status != "responding" {
		t.Fatalf("setup: status = %q, want responding", m.status)
	}

	msgHeight := m.messageRegionHeight()
	// The painter scrolls in hardware, so the viewport pins exactly to the
	// bottom on every append — the newest line is always the last visible row.
	for i := 0; i < 20; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "streamed line\n"})
		lines, _ := m.buildFlatRenderLines(msgHeight)
		if len(lines) == 0 {
			t.Fatal("no visible lines")
		}
		wantStart := m.totalRenderedLines() - msgHeight
		if got := lines[0].SourceLine; got != wantStart {
			t.Fatalf("append %d: viewport start %d, want exact bottom pin %d", i, got, wantStart)
		}
		last := lines[len(lines)-1]
		if last.SourceLine != m.blocks[0].cachedLineCount-1 {
			t.Fatalf("append %d: newest line not visible (last=%d, want %d)",
				i, last.SourceLine, m.blocks[0].cachedLineCount-1)
		}
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

// markdownHeavyMessage builds a ~targetBytes-long message of headings,
// paragraphs, bullet lists and fenced code blocks — repeating a fixed
// "unit" of markdown until the target size is reached — for the progressive
// markdown streaming benchmarks below.
func markdownHeavyMessage(targetBytes int) string {
	unit := "## Section heading\n\n" +
		"Some prose that explains the section in a paragraph long enough to " +
		"wrap across several lines at a typical terminal width, giving the " +
		"raw-wrap and glamour-wrap paths a realistic amount of text to chew on.\n\n" +
		"- first bullet point with a bit of detail\n" +
		"- second bullet point, also with some detail\n" +
		"- third bullet point rounding out the list\n\n" +
		"```go\n" +
		"func handle(ctx context.Context, req *Request) (*Response, error) {\n" +
		"\tif req == nil {\n" +
		"\t\treturn nil, errors.New(\"nil request\")\n" +
		"\t}\n" +
		"\treturn &Response{OK: true}, nil\n" +
		"}\n" +
		"```\n\n"
	var b strings.Builder
	for b.Len() < targetBytes {
		b.WriteString(unit)
	}
	return b.String()
}

// benchmarkStreamProgressiveMarkdown streams content into a single text
// block in 6-byte chunks via appendOrAppend (two renders per token, per
// appendOrAppend's own accounting) — the same harness shape the item-51
// review's F1 finding used to measure the streaming hot path's cost.
func benchmarkStreamProgressiveMarkdown(b *testing.B, content string) {
	b.ReportAllocs()
	for b.Loop() {
		m := newModel(make(chan string, 1), "test-model", "", nil, 0, 0, 0)
		m.width = 100
		m.height = 24
		m.status = "responding"
		for i := 0; i < len(content); i += 6 {
			end := i + 6
			if end > len(content) {
				end = len(content)
			}
			m.appendOrAppend("text", content[i:end])
		}
	}
}

func BenchmarkStreamProgressiveMarkdown10KB(b *testing.B) {
	benchmarkStreamProgressiveMarkdown(b, markdownHeavyMessage(10*1024))
}

func BenchmarkStreamProgressiveMarkdown20KB(b *testing.B) {
	benchmarkStreamProgressiveMarkdown(b, markdownHeavyMessage(20*1024))
}
