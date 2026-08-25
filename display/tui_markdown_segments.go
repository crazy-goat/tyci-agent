package display

import "strings"

// mdStreamState tracks, per streaming text block, how much of the block's
// content is safe to glamour-render progressively.
//
// The safe-boundary rule: a "\n\n" (a blank line) is safe to flush on iff no
// fenced code block is open at that point. Nothing else needs tracking:
//
//   - GFM tables need no state — a table row is a single line and a table
//     ends at the first line that is not a row, so a blank line is always
//     already past the end of any table.
//   - Fences are the only construct spanning blank lines with no
//     single-line terminator, so their open/close state is the only thing
//     that must be carried across scans.
//
// Indented (4-space) code blocks, front matter, and setext headings are
// deliberately not tracked — see TODO item 51's design notes. Any
// imperfection here self-heals: forceRenderDirtyBlocks re-renders the whole
// block from scratch once it finishes.
type mdStreamState struct {
	scanPos     int  // bytes of content already scanned for boundaries/fences
	inFence     bool // true while a fenced code block is open
	fenceMarker byte // '`' or '~' of the currently open fence
	fenceLen    int  // opening run length of the currently open fence
	fenceIndent int  // indent (0-3) of the currently open fence's opening line

	safeUpto     int // byte offset of the latest known-safe flush point
	renderedUpto int // byte offset up to which content has been glamour-rendered

	// renderedPrefixLines holds the glamour-rendered, already-wrapped lines
	// for content[:renderedUpto]. Stable once written; never re-rendered.
	renderedPrefixLines []string
}

// fenceLineInfo inspects a single logical line (no trailing '\n') for fence
// marker syntax: up to 3 leading spaces, then a run of 3+ identical '`' or
// '~' characters. Tabs and indents beyond 3 spaces are deliberately not
// treated as fence lines (out of scope — see mdStreamState's doc comment).
func fenceLineInfo(line string) (indent int, marker byte, run int, rest string, ok bool) {
	n := len(line)
	i := 0
	for i < n && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= n || line[i] == ' ' {
		// Either no content after the indent, or a 4th leading space —
		// indented code block territory, not a fence.
		return 0, 0, 0, "", false
	}
	c := line[i]
	if c != '`' && c != '~' {
		return 0, 0, 0, "", false
	}
	j := i
	for j < n && line[j] == c {
		j++
	}
	run = j - i
	if run < 3 {
		return 0, 0, 0, "", false
	}
	return i, c, run, line[j:], true
}

// scan incrementally advances the fence/boundary scan over new content,
// updating inFence/fenceMarker/fenceLen/fenceIndent and safeUpto. It only
// looks at bytes after scanPos and only advances scanPos to just past the
// last '\n' in content — the same anchor streamWrap uses — so it never
// re-scans history and costs O(new bytes), not O(content).
func (s *mdStreamState) scan(content string) {
	nl := strings.LastIndexByte(content, '\n')
	if nl+1 <= s.scanPos {
		return
	}
	segment := content[s.scanPos : nl+1]
	lines := strings.Split(segment, "\n")
	lines = lines[:len(lines)-1] // trailing "" after the final '\n'
	pos := s.scanPos
	for _, line := range lines {
		lineEnd := pos + len(line) + 1 // +1 for the '\n'
		if s.inFence {
			if indent, marker, run, rest, ok := fenceLineInfo(line); ok &&
				marker == s.fenceMarker && run >= s.fenceLen &&
				strings.TrimSpace(rest) == "" {
				_ = indent
				s.inFence = false
			}
		} else if indent, marker, run, _, ok := fenceLineInfo(line); ok {
			s.inFence = true
			s.fenceMarker = marker
			s.fenceLen = run
			s.fenceIndent = indent
		} else if line == "" {
			// A blank line while no fence is open: safe to flush everything
			// up to and including this blank line's terminating '\n'.
			s.safeUpto = lineEnd
		}
		pos = lineEnd
	}
	s.scanPos = nl + 1
}

// renderStreamingMarkdown renders block idx's streaming text content as a
// stable, glamour-styled prefix plus a raw wrapped tail.
//
// The prefix grows only up to the latest safe "\n\n" boundary found by
// mdStreamState.scan (no fence open at that point); each newly safe segment
// is glamour-rendered exactly once and its lines are appended to
// renderedPrefixLines, which is never touched again. The tail —
// content[renderedUpto:] — is re-wrapped incrementally by a streamWrap the
// same way the whole block used to be, so a message with no safe boundary at
// all (one giant paragraph, one giant fence) produces byte-identical output
// to the pre-existing pure streamWrap path: renderedPrefixLines stays empty
// and the composed lines are exactly the tail's lines.
//
// Every byte reaches glamour at most once: segments partition the content,
// and a glamour call only fires on the rare delta that completes a paragraph
// (O(that paragraph)), never on the common per-token append (O(1) amortized,
// same as the streaming hot path this feeds — see issue #84 in
// tui_blocks.go's appendOrAppend).
func (m *TuiModel) renderStreamingMarkdown(idx int, content string) string {
	st := m.mdStreamState[idx]
	if st == nil {
		st = &mdStreamState{}
		m.mdStreamState[idx] = st
	}
	st.scan(content)

	width := m.renderWidth()
	if st.safeUpto > st.renderedUpto {
		segment := content[st.renderedUpto:st.safeUpto]
		rendered := renderMarkdownWithCache(segment, false, width)
		if rendered != "" {
			st.renderedPrefixLines = append(st.renderedPrefixLines, strings.Split(rendered, "\n")...)
		}
		st.renderedUpto = st.safeUpto
		// The tail wrapper's offsets are relative to whatever string it's
		// fed; renderedUpto just moved, so the growing suffix it will be fed
		// changed underneath it — start a fresh one.
		m.streamWraps[idx] = &streamWrap{}
	}

	sw := m.streamWraps[idx]
	if sw == nil {
		sw = &streamWrap{}
		m.streamWraps[idx] = sw
	}
	// A tail of "" (everything flushed into the prefix, nothing pending)
	// contributes zero lines — not one blank line. streamWrap.render always
	// returns at least one ([""]) line for its input because content == ""
	// never used to reach it (tryRenderMarkdown short-circuits an empty
	// whole-block content before ever calling this function); here the
	// *tail* legitimately can be empty once a flush lands exactly at the
	// block's current end, and padding it with a spurious blank line would
	// make cachedLineCount overcount relative to what actually renders.
	var tailLines []string
	if tail := content[st.renderedUpto:]; tail != "" {
		// renderWidth, not m.width: with the sidebar open the only thing on
		// screen is the narrowed main column, so this cache must hold lines
		// wrapped at mainColumnWidth (see renderWidth). Caching full-width
		// lines here is what shredded markdown tables under the sidebar's
		// safety re-wrap.
		_, tailLines = sw.render(tail, false, width)
	}

	lines := make([]string, 0, len(st.renderedPrefixLines)+len(tailLines))
	lines = append(lines, st.renderedPrefixLines...)
	lines = append(lines, tailLines...)
	wrapped := strings.Join(lines, "\n")

	m.blocks[idx].cachedLineCount = len(lines)
	m.blocks[idx].cachedLines = lines
	return wrapped
}
