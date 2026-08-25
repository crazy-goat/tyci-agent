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
		{"    ```", false, 0, 0, 0}, // 4-space indent: indented code, not a fence
		{"``", false, 0, 0, 0},      // run too short
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
