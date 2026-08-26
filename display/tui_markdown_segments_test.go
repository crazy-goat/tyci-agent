package display

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// ─── fenceLineInfo / mdStreamState.scan — the fence state machine ────────

// TestFenceLineInfo checks the low-level fence-marker line parser in
// isolation: indent limits, marker character, and run length.
func TestFenceLineInfo(t *testing.T) {
	cases := []struct {
		line       string
		wantOK     bool
		wantMarker byte
		wantRun    int
		wantIndent int
	}{
		{"```", true, '`', 3, 0},
		{"````", true, '`', 4, 0},
		{"~~~", true, '~', 3, 0},
		{" ```", true, '`', 3, 1},
		{"   ```", true, '`', 3, 3},
		// F3 (item-51 review): indentation is no longer capped at 3 — a
		// fence nested inside a list item is indented well past column 3 in
		// the raw text, and capping it made such fences invisible to the
		// scanner. The tradeoff (a 4-space indented plain-text code block
		// that happens to contain a marker run gets misdetected) is an
		// accepted, self-healing edge case.
		{"    ```", true, '`', 3, 4},
		{"     ```", true, '`', 3, 5},
		{"\t```", true, '`', 3, 1},
		{"``", false, 0, 0, 0}, // run too short
		{"~~", false, 0, 0, 0},
		{"plain text", false, 0, 0, 0},
		{"", false, 0, 0, 0},
		{"```go", true, '`', 3, 0}, // info string after opening run
	}
	for _, c := range cases {
		indent, marker, run, _, ok := fenceLineInfo(c.line)
		if ok != c.wantOK {
			t.Errorf("fenceLineInfo(%q) ok = %v, want %v", c.line, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if marker != c.wantMarker || run != c.wantRun || indent != c.wantIndent {
			t.Errorf("fenceLineInfo(%q) = (indent=%d, marker=%q, run=%d), want (indent=%d, marker=%q, run=%d)",
				c.line, indent, marker, run, c.wantIndent, c.wantMarker, c.wantRun)
		}
	}
}

// TestFenceKeepsSafeUptoBeforeFence feeds a fence with an interior blank
// line byte-by-byte (so the marker itself is split across scan calls, the
// classic "mid-marker" chunk boundary) and asserts safeUpto never advances
// past the point right before the fence opened until the fence has
// legitimately closed and a real blank line follows it.
func TestFenceKeepsSafeUptoBeforeFence(t *testing.T) {
	prefix := "prefix\n\n"
	fenceOpen := "```\n"
	codeBody := "code line 1\n\ncode line 2\n" // blank line INSIDE the fence
	fenceClose := "```\n"
	tailBlank := "\n"
	after := "after\n"
	content := prefix + fenceOpen + codeBody + fenceClose + tailBlank + after

	closedAt := len(prefix + fenceOpen + codeBody + fenceClose)
	safeAfterClose := len(prefix + fenceOpen + codeBody + fenceClose + tailBlank)

	st := &mdStreamState{}
	var built strings.Builder
	for i := 0; i < len(content); i++ {
		built.WriteByte(content[i])
		st.scan(built.String())
		pos := built.Len()
		if pos <= closedAt && st.safeUpto > len(prefix) {
			t.Fatalf("byte %d: safeUpto=%d advanced past/into the open fence (prefix ends at %d)",
				pos, st.safeUpto, len(prefix))
		}
	}
	if st.inFence {
		t.Fatal("fence should be closed by end of content")
	}
	if st.safeUpto != safeAfterClose {
		t.Fatalf("safeUpto=%d, want %d (right after the fence-close blank line)", st.safeUpto, safeAfterClose)
	}
}

// TestFenceCloseRequiresRunLengthAtLeastOpening is the classic first bug in
// this kind of scanner: a fence closes only on a run of the same marker
// character with length >= the opening run. A shorter run of the same
// character inside the fence must NOT close it.
func TestFenceCloseRequiresRunLengthAtLeastOpening(t *testing.T) {
	for _, marker := range []string{"`", "~"} {
		open := strings.Repeat(marker, 5) + "\n"
		shortRun := strings.Repeat(marker, 3) + "\n" // shorter run: must not close
		close5 := strings.Repeat(marker, 5) + "\n"
		content := "before\n\n" + open + shortRun + "still open\n" + close5 + "\nafter\n"

		st := &mdStreamState{}
		afterOpen := len("before\n\n" + open)
		afterShortRun := afterOpen + len(shortRun)

		st.scan(content[:afterOpen])
		if !st.inFence {
			t.Fatalf("marker=%q: fence should be open right after the opening run", marker)
		}
		st.scan(content[:afterShortRun])
		if !st.inFence {
			t.Fatalf("marker=%q: a %d-run must NOT close a 5-run fence", marker, 3)
		}
		st.scan(content)
		if st.inFence {
			t.Fatalf("marker=%q: a matching 5-run must close the fence", marker)
		}
	}
}

// ─── GFM tables need no fence-like state ─────────────────────────────────

func TestTableNeedsNoFenceState(t *testing.T) {
	// Full table (header + delimiter + rows) followed by a blank line: the
	// entire table plus the blank line is one safe segment. No premature
	// flush partway through the table, and the flush happens right after
	// the blank line completes it.
	table := "| a | b |\n| - | - |\n| 1 | 2 |\n| 3 | 4 |\n\n"
	st := &mdStreamState{}
	var built strings.Builder
	rows := strings.SplitAfter(table, "\n")
	for _, r := range rows {
		if r == "" {
			continue
		}
		built.WriteString(r)
		st.scan(built.String())
		if built.String() != table && st.safeUpto != 0 {
			t.Fatalf("premature flush before the table's blank line: safeUpto=%d at %q", st.safeUpto, built.String())
		}
	}
	if st.safeUpto != len(table) {
		t.Fatalf("safeUpto=%d, want %d (whole table + trailing blank)", st.safeUpto, len(table))
	}

	// A lone "|" line with no delimiter row was never a table — the blank
	// line right after it flushes immediately, same as any ordinary text.
	headerOnly := "| a | b |\n\n"
	st2 := &mdStreamState{}
	st2.scan(headerOnly)
	if st2.safeUpto != len(headerOnly) {
		t.Fatalf("header-only+blank: safeUpto=%d, want %d (flushes immediately)", st2.safeUpto, len(headerOnly))
	}
}

// ─── No blank line at all: must match pure streamWrap byte-for-byte ─────

// TestNoBoundaryMatchesPureStreamWrap is the no-dead-air regression guard:
// a message with no "\n\n" anywhere (one giant paragraph, or one giant open
// fence) must never hold anything back in renderedPrefixLines, and the
// composed output must be byte-identical to what the pre-existing pure
// streamWrap path produced.
func TestNoBoundaryMatchesPureStreamWrap(t *testing.T) {
	cases := map[string]string{
		"giant paragraph":  strings.Repeat("word ", 400),
		"giant open fence": "```go\n" + strings.Repeat("line of code\n", 60), // never closes, no blank line
	}
	width := 60
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			m := newModel(make(chan string, 1), "test-model", "", nil, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
			m.width = width
			m.height = 24
			m.status = "responding"
			m.blocks = append(m.blocks, block{kind: "text", dirty: true})
			idx := 0
			m.dirtyBlocks[idx] = true

			for i := 0; i < len(content); i += 11 {
				end := i + 11
				if end > len(content) {
					end = len(content)
				}
				m.blocks[idx].content += content[i:end]
				m.blocks[idx].dirty = true
				m.dirtyBlocks[idx] = true
				m.blocks[idx].cachedLines = nil
				_ = m.renderBlock(idx, m.blocks[idx])
			}

			if st := m.mdStreamState[idx]; st != nil && len(st.renderedPrefixLines) != 0 {
				t.Fatalf("renderedPrefixLines should stay empty with no safe boundary, got %d lines",
					len(st.renderedPrefixLines))
			}

			want := wrapRawText(content, false, width)
			got := strings.Join(m.blocks[idx].cachedLines, "\n")
			if got != want {
				t.Fatalf("progressive output diverged from pure streamWrap:\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// ─── Ordered-list numbering across a flush ───────────────────────────────

// TestOrderedListNumberingAcrossFlush records the measured ground truth for
// what happens to ordered-list numbering when a flush lands between list
// items: goldmark renders each independent segment starting at that
// segment's own *literal* first number (it does not restart numbering at 1).
// So "3. three" flushed as its own segment renders as "3. three", not
// "1. three" — this is not a defect to fix, just documented behavior.
func TestOrderedListNumberingAcrossFlush(t *testing.T) {
	m := newModel(make(chan string, 1), "test-model", "", nil, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	m.height = 24
	m.status = "responding"
	idx := 0
	m.blocks = append(m.blocks, block{kind: "text", dirty: true})
	m.dirtyBlocks[idx] = true

	chunks := []string{"1. one\n2. two\n\n", "3. three\n\n", "Done"}
	for _, c := range chunks {
		m.blocks[idx].content += c
		m.blocks[idx].dirty = true
		m.dirtyBlocks[idx] = true
		m.blocks[idx].cachedLines = nil
		_ = m.renderBlock(idx, m.blocks[idx])
	}

	st := m.mdStreamState[idx]
	if st == nil || len(st.renderedPrefixLines) == 0 {
		t.Fatal("expected the first two list items to have been flushed into the styled prefix")
	}
	plain := stripAnsi(strings.Join(st.renderedPrefixLines, "\n"))
	if !strings.Contains(plain, "1. one") || !strings.Contains(plain, "2. two") {
		t.Errorf("first segment lost its literal item numbers: %q", plain)
	}
	if !strings.Contains(plain, "3. three") {
		// Ground truth: goldmark preserves the literal starting number of an
		// isolated list segment, so this must read "3. three", not "1. three".
		t.Errorf("second segment did not preserve its literal starting number: %q", plain)
	}
}

// ─── CJK / emoji width at a flush boundary ───────────────────────────────

// TestCJKEmojiNoTruncationAtBoundary streams wide-rune content (CJK plus
// emoji) fed in small byte chunks that routinely split multi-byte
// characters mid-rune, with a flush boundary in the middle. Segmentation by
// itself must never truncate a rune or leave the invalid-UTF-8 replacement
// character in the output.
func TestCJKEmojiNoTruncationAtBoundary(t *testing.T) {
	content := "第一段文字很长很长很长很长 🎉🎉🎉 more emoji 😀😃😄\n\n" +
		"第二段 continues 你好世界 🚀\n"

	for _, chunkSize := range []int{1, 2, 3, 5} {
		m := newModel(make(chan string, 1), "test-model", "", nil, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
		m.width = 40
		m.height = 24
		m.status = "responding"
		idx := 0
		m.blocks = append(m.blocks, block{kind: "text", dirty: true})
		m.dirtyBlocks[idx] = true

		for i := 0; i < len(content); i += chunkSize {
			end := i + chunkSize
			if end > len(content) {
				end = len(content)
			}
			m.blocks[idx].content += content[i:end]
			m.blocks[idx].dirty = true
			m.dirtyBlocks[idx] = true
			m.blocks[idx].cachedLines = nil
			_ = m.renderBlock(idx, m.blocks[idx])
		}

		out := strings.Join(m.blocks[idx].cachedLines, "\n")
		if !utf8.ValidString(out) {
			t.Errorf("chunkSize=%d: output is not valid UTF-8", chunkSize)
		}
		if strings.ContainsRune(out, utf8.RuneError) {
			t.Errorf("chunkSize=%d: output contains the UTF-8 replacement character (truncated rune)", chunkSize)
		}
	}
}

// ─── Chunk-boundary independence ─────────────────────────────────────────

// TestScanIsChunkBoundaryIndependent extends the idea behind
// TestStreamWrapMatchesFullWrap to the fence/boundary scanner: identical
// content fed byte-by-byte, word-by-word, and as one chunk must yield an
// identical final safeUpto/inFence, regardless of where the chunk splits
// happened to fall.
func TestScanIsChunkBoundaryIndependent(t *testing.T) {
	content := "# Title\n\nSome text.\n\n```go\nfunc main() {}\n```\n\n" +
		"| a | b |\n| - | - |\n| 1 | 2 |\n\n" +
		"final paragraph with no trailing blank line"

	feed := func(chunks []string) *mdStreamState {
		st := &mdStreamState{}
		var built strings.Builder
		for _, c := range chunks {
			built.WriteString(c)
			st.scan(built.String())
		}
		return st
	}

	byteChunks := make([]string, len(content))
	for i, r := range []byte(content) {
		byteChunks[i] = string(r)
	}
	wordChunks := strings.SplitAfter(content, " ")
	oneChunk := []string{content}

	want := feed(oneChunk)
	for name, chunks := range map[string][]string{"byte-by-byte": byteChunks, "word-by-word": wordChunks} {
		got := feed(chunks)
		if got.safeUpto != want.safeUpto {
			t.Errorf("%s: safeUpto=%d, want %d", name, got.safeUpto, want.safeUpto)
		}
		if got.inFence != want.inFence {
			t.Errorf("%s: inFence=%v, want %v", name, got.inFence, want.inFence)
		}
	}
}

// ─── Partition guard: every byte reaches glamour at most once ───────────

// TestSegmentPartitionCoversContentExactlyOnce verifies the segmentation
// invariant that makes renderStreamingMarkdown O(delta) instead of O(#84):
// as safeUpto advances, the segments handed to glamour (content[old
// renderedUpto:new safeUpto]) partition the content exactly — their total
// length plus the final unflushed tail equals the content length, for any
// chunk size.
func TestSegmentPartitionCoversContentExactlyOnce(t *testing.T) {
	content := markdownHeavyContent
	for _, chunkSize := range []int{1, 3, 7, 50} {
		st := &mdStreamState{}
		renderedUpto := 0
		totalSegBytes := 0
		var built strings.Builder
		for i := 0; i < len(content); i += chunkSize {
			end := i + chunkSize
			if end > len(content) {
				end = len(content)
			}
			built.WriteString(content[i:end])
			st.scan(built.String())
			if st.safeUpto > renderedUpto {
				totalSegBytes += st.safeUpto - renderedUpto
				renderedUpto = st.safeUpto
			}
		}
		tailBytes := len(content) - renderedUpto
		if totalSegBytes+tailBytes != len(content) {
			t.Errorf("chunkSize=%d: segments (%d bytes) + tail (%d bytes) = %d, want content length %d",
				chunkSize, totalSegBytes, tailBytes, totalSegBytes+tailBytes, len(content))
		}
	}
}

// ─── F16(a): the composer's tailWrapped/tailLines invariant ──────────────

// TestStreamWrapRender_CanReturnEmptyOutWithNonEmptyLines pins down the
// low-level fact renderStreamingMarkdown's composer used to silently rely
// on: streamWrap.render's own `if out == "" { lines = []string{""} }`
// (tui_render_block.go) means content that wraps to nothing (every logical
// line empty — reachable only when content is composed entirely of bare
// "\n"s) comes back as out="" but a NON-empty, one-element lines slice.
// "len(tailLines) > 0" is therefore not proof that "tailWrapped != ''".
func TestStreamWrapRender_CanReturnEmptyOutWithNonEmptyLines(t *testing.T) {
	sw := &streamWrap{}
	out, lines := sw.render("\n", false, 80)
	if out != "" {
		t.Fatalf("expected out == \"\" for an all-newline tail, got %q", out)
	}
	if len(lines) == 0 {
		t.Fatalf("expected a non-empty lines slice even though out == \"\", got %v", lines)
	}
}

// TestRenderStreamingMarkdown_NonEmptyPrefixSurvivesBlankTail is F16(a)'s
// regression test at renderStreamingMarkdown's level: with a styled prefix
// already flushed and a pending tail that happens to be pure "\n" (so
// streamWrap.render returns "" per the test above), the function's return
// value is only ever consulted by getBlockLines for an `== ""` emptiness
// check (see its doc comment) — if it wrongly returns "" here, getBlockLines
// wipes the cachedLines this call just populated with real, non-empty
// prefix content, vanishing an already-styled block for a frame.
//
// The mdStreamState fields are set directly (scanPos already past the
// tail, safeUpto == renderedUpto so no new flush fires this call) to
// isolate the composer from scan()'s own behavior — in ordinary streaming
// scan() sweeps a completed all-newline segment into safeUpto immediately,
// so this exact state is a boundary case of the type's contract, not
// something scan() itself produces; the composer must still handle it
// correctly rather than trust an invariant scan() doesn't actually
// guarantee syntactically.
func TestRenderStreamingMarkdown_NonEmptyPrefixSurvivesBlankTail(t *testing.T) {
	m := newModel(make(chan string, 1), "test-model", "", nil, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 80
	m.height = 24
	m.status = "responding"
	m.blocks = append(m.blocks, block{kind: "text", dirty: true})
	idx := 0
	m.dirtyBlocks[idx] = true

	flushed := "already flushed prefix\n"
	content := flushed + "\n" // pending tail: a single bare newline, content[len(flushed):] == "\n"
	st := &mdStreamState{
		renderedPrefixLines:  []string{"styled prefix line"},
		renderedPrefixJoined: "styled prefix line\n",
		renderedUpto:         len(flushed),
		// scanPos == len(content) (computed from the FINAL content, tail
		// included) makes st.scan(content) a no-op inside
		// renderStreamingMarkdown below, so this test isolates the
		// composer's handling of the tail from scan()'s own behavior —
		// which would otherwise immediately sweep this trailing "\n" into
		// safeUpto itself (see the test's doc comment).
		scanPos:  len(content),
		safeUpto: len(flushed),
	}
	m.mdStreamState[idx] = st

	got := m.renderStreamingMarkdown(idx, content)
	if got == "" {
		t.Fatal("renderStreamingMarkdown returned \"\" despite a non-empty styled prefix — " +
			"getBlockLines would read this as \"nothing rendered\" and wipe the cachedLines just set")
	}
	if len(m.blocks[idx].cachedLines) == 0 {
		t.Fatalf("expected cachedLines to hold the flushed prefix, got %v", m.blocks[idx].cachedLines)
	}
}

// ─── Self-healing: final render must match the unstreamed render ────────

// TestFinalRenderMatchesUnstreamed is the property that operationalizes
// self-healing for this whole design: for every fixture, once the block
// finishes (forceRenderDirtyBlocks), the render must be byte-identical to
// rendering the same content once, unstreamed — regardless of what the
// progressive path did while streaming, and regardless of chunk size.
func TestFinalRenderMatchesUnstreamed(t *testing.T) {
	fixtures := map[string]string{
		"headings/bullets/fence": markdownHeavyContent,
		"table":                  "| a | b |\n| - | - |\n| 1 | 2 |\n\nAfter the table.\n",
		"ordered list flush":     "1. one\n2. two\n\n3. three\n\nDone",
		"cjk emoji":              "第一段文字 🎉🎉🎉\n\n第二段 你好世界 🚀\n",
		"no boundary":            strings.Repeat("word ", 100),
		// The three constructs the owner explicitly accepted mid-stream
		// degradation for — pinned here per the item-51 review, rather than
		// left only in a reviewer's scratch probe.
		"nested lists": "- outer one\n  - inner a\n  - inner b\n- outer two\n\n" +
			"1. step one\n   1. sub step\n2. step two\n\nDone.\n",
		"links plus inline code": "See [the docs](https://example.com/docs) for `Config.Load()` details.\n\n" +
			"Also check `pkg.Init` and [this issue](https://example.com/issues/1).\n",
		"blockquotes": "> First quoted line.\n> Second quoted line.\n\n" +
			"Normal paragraph after the quote.\n\n> Another quote\n>\n> with a blank line inside it\n\nDone.\n",
	}

	newStreamedModel := func() *TuiModel {
		m := newModel(make(chan string, 1), "test-model", "", nil, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
		m.width = 70
		m.height = 24
		m.status = "responding"
		return &m
	}

	for name, content := range fixtures {
		t.Run(name, func(t *testing.T) {
			for _, chunkSize := range []int{1, 4, 17} {
				// Streamed: fed in chunks, then finalized.
				streamed := newStreamedModel()
				streamed.blocks = append(streamed.blocks, block{kind: "text", dirty: true})
				streamed.dirtyBlocks[0] = true
				for i := 0; i < len(content); i += chunkSize {
					end := i + chunkSize
					if end > len(content) {
						end = len(content)
					}
					streamed.appendOrAppend("text", content[i:end])
				}
				streamed.status = "idle"
				streamed.forceRenderDirtyBlocks()
				streamedOut := strings.Join(streamed.blocks[0].cachedLines, "\n")

				// Unstreamed: whole content added at once, then finalized.
				unstreamed := newStreamedModel()
				unstreamed.status = "idle"
				unstreamed.blocks = append(unstreamed.blocks, block{kind: "text", content: content, dirty: true})
				unstreamed.dirtyBlocks[0] = true
				unstreamed.forceRenderDirtyBlocks()
				unstreamedOut := strings.Join(unstreamed.blocks[0].cachedLines, "\n")

				if streamedOut != unstreamedOut {
					t.Errorf("chunkSize=%d: streamed final render diverged from unstreamed render\nstreamed:   %q\nunstreamed: %q",
						chunkSize, streamedOut, unstreamedOut)
				}
			}
		})
	}
}

// ─── F2: blank-line separator between flushed paragraphs ────────────────

// TestFlushKeepsBlankSeparatorBetweenParagraphs streams four paragraphs and
// checks, mid-stream (before finalization), that flushed paragraphs stay
// visually separated by a blank line instead of jamming together — the F2
// fix from the item-51 review.
func TestFlushKeepsBlankSeparatorBetweenParagraphs(t *testing.T) {
	paragraphs := []string{"Paragraph one.", "Paragraph two.", "Paragraph three.", "Paragraph four."}
	content := strings.Join(paragraphs, "\n\n") + "\n\n"

	m := newModel(make(chan string, 1), "test-model", "", nil, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 60
	m.height = 24
	m.status = "responding"
	idx := 0
	m.blocks = append(m.blocks, block{kind: "text", dirty: true})
	m.dirtyBlocks[idx] = true

	for i := 0; i < len(content); i += 5 {
		end := i + 5
		if end > len(content) {
			end = len(content)
		}
		m.blocks[idx].content += content[i:end]
		m.blocks[idx].dirty = true
		m.dirtyBlocks[idx] = true
		m.blocks[idx].cachedLines = nil
		_ = m.renderBlock(idx, m.blocks[idx])
	}

	plain := stripAnsi(strings.Join(m.blocks[idx].cachedLines, "\n"))
	blankRuns := strings.Count(plain, "\n\n")
	if blankRuns < len(paragraphs)-1 {
		t.Errorf("expected at least %d blank-line separators mid-stream, found %d in:\n%s",
			len(paragraphs)-1, blankRuns, plain)
	}
}

// TestFlushSeparatorTableStillHasOwnPadding checks that F2's added separator
// doesn't stack with glamour's own leading pad line for a table, which would
// otherwise make tables get double spacing while other constructs get one —
// the review's "worse than uniform" observation about the pre-fix state.
func TestFlushSeparatorTableStillHasOwnPadding(t *testing.T) {
	content := "| a | b |\n| - | - |\n| 1 | 2 |\n\nAfter the table.\n"
	m := newModel(make(chan string, 1), "test-model", "", nil, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 60
	m.height = 24
	m.status = "responding"
	idx := 0
	m.blocks = append(m.blocks, block{kind: "text", content: content, dirty: true})
	m.dirtyBlocks[idx] = true
	_ = m.renderBlock(idx, m.blocks[idx])

	plain := stripAnsi(strings.Join(m.blocks[idx].cachedLines, "\n"))
	if strings.Contains(plain, "\n\n\n") {
		t.Errorf("table segment produced 2+ blank lines instead of a uniform single separator:\n%q", plain)
	}
}

// ─── F3: fence nested inside a list item ─────────────────────────────────

// TestNestedFenceInsideListItemDoesNotFlushMidCode is the exact probe from
// the item-51 review: a numbered step whose sub-bullet holds a fenced code
// block indented well past column 3. Before F3, the indented opening marker
// was invisible to the scanner, so the blank line inside the "code" was
// wrongly treated as a safe flush point and the closing marker was never
// recognized (inFence stuck false forever).
func TestNestedFenceInsideListItemDoesNotFlushMidCode(t *testing.T) {
	content := "1. a\n   - b\n     ```\n     code\n\n     more\n     ```\n\nX\n"
	// "X\n" at the end has no following blank line, so it's the unflushed
	// tail — the last safe boundary is right after the fence closes and its
	// blank line, i.e. everything except "X\n".
	wantSafeUpto := len(content) - len("X\n")

	st := &mdStreamState{}
	st.scan(content)

	if st.inFence {
		t.Fatal("fence should be closed by end of content")
	}
	if st.safeUpto != wantSafeUpto {
		t.Fatalf("safeUpto=%d, want %d (everything up to and including the fence-close blank line)",
			st.safeUpto, wantSafeUpto)
	}

	// The blank line strictly inside the fence (between "code" and "more")
	// must not have set safeUpto on its own.
	midFence := strings.Index(content, "more") // position is after the interior blank line
	st2 := &mdStreamState{}
	st2.scan(content[:midFence])
	if st2.safeUpto != 0 {
		t.Fatalf("interior blank line inside the nested fence set safeUpto=%d, want 0 (fence still open)",
			st2.safeUpto)
	}
	if !st2.inFence {
		t.Fatal("fence should still be open right after its interior blank line")
	}
}

// ─── F4: whitespace-only and CRLF blank lines ────────────────────────────

// TestWhitespaceOnlyAndCRLFLinesAreBlank checks that a line of only spaces,
// and a CRLF-terminated blank line (whose "line" content is a bare "\r"),
// both count as safe flush points — before F4 both failed safe, and with
// CRLF input progressive rendering never engaged at all.
func TestWhitespaceOnlyAndCRLFLinesAreBlank(t *testing.T) {
	t.Run("whitespace-only line", func(t *testing.T) {
		content := "a\n   \nb\n"
		st := &mdStreamState{}
		st.scan(content)
		if st.safeUpto == 0 {
			t.Errorf("a whitespace-only line should count as a blank line, safeUpto=%d", st.safeUpto)
		}
	})
	t.Run("CRLF blank line", func(t *testing.T) {
		content := "a\r\n\r\nb\r\n\r\n"
		st := &mdStreamState{}
		st.scan(content)
		if st.safeUpto == 0 {
			t.Errorf("a CRLF blank line should count as a blank line, safeUpto=%d", st.safeUpto)
		}
	})
}

// ─── F6: shrink guard, symmetric with streamWrap.render's ────────────────

// TestRenderStreamingMarkdownRecoversFromContentShrink mirrors
// TestStreamWrapRecoversFromContentShrink: if content ever shrinks
// (renderedUpto or scanPos would otherwise exceed len(content)), the state
// must restart from scratch instead of panicking on a bad slice.
func TestRenderStreamingMarkdownRecoversFromContentShrink(t *testing.T) {
	m := newModel(make(chan string, 1), "test-model", "", nil, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width = 60
	m.height = 24
	m.status = "responding"
	idx := 0
	long := "first paragraph\n\nsecond paragraph that is long enough to flush\n\nthird one\n"
	m.blocks = append(m.blocks, block{kind: "text", content: long, dirty: true})
	m.dirtyBlocks[idx] = true

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("content shrink panicked: %v", r)
		}
	}()

	_ = m.renderBlock(idx, m.blocks[idx])
	if st := m.mdStreamState[idx]; st == nil || st.renderedUpto == 0 {
		t.Fatal("setup: expected at least one flush before shrinking content")
	}

	// Content shrinks — must not panic, and must recover cleanly.
	m.blocks[idx].content = "short"
	m.blocks[idx].dirty = true
	m.dirtyBlocks[idx] = true
	m.blocks[idx].cachedLines = nil
	_ = m.renderBlock(idx, m.blocks[idx])

	got := strings.Join(m.blocks[idx].cachedLines, "\n")
	want := wrapRawText("short", false, m.renderWidth())
	if got != want {
		t.Errorf("after shrink got %q, want %q", got, want)
	}
}
